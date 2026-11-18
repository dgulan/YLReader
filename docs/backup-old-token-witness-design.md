# 同次旧令牌拒绝观察（Access + Refresh）并入通用行为见证设计（H2）

状态：H2 内部先行设计，不修改 43 个公开 API、`docs/schema.sql`、备份归档格式、16 份冻结契约摘要或旧 `isolated-http-fixture-v1` 报告；`reader_roundtrip` 在本阶段仍保持 `unavailable`，`bound-behavior-review` 的 `status` 仍为 `partial`、`execution_authorized`/`ready_for_cutover`/`deployment_authorized` 仍为 `false`，且暂不接入 CAS 登记（留待 H4）。

## 1. 目标与边界

将同一 `restoreGuard` 生命周期内全部白名单用户的旧 `access_token`（`GET /api/v1/auth/me` -> `401`）与旧 `refresh_token`（`POST /api/v1/auth/refresh` -> `401`）双拒绝确认并入 `confirmedBehaviorRun` 内存见证，并在满足全部绑定与时效条件时由 `collectBoundBehaviorReview` 将 `bound-https-account-verification-v1` 报告的 `old_token_http_rejection` 检查项置为 `pass`（`11 pass / 1 unavailable`）。

- `confirmedBehaviorRun` 与 `oldTokenProbeWitness` 均为私有内存结构（禁止 JSON 序列化，且不可从磁盘文件或历史状态重建）。
- 禁止拼接旧 `isolated-http-fixture-v1` 报告或任何独立运行窗口的探测结果。
- 不修改 `behavior_old_access.go` 既有 `rejectOldAccess` 方法语义，在其外层增加绑定核验包装 `confirmOldAccess`，确保既有用例兼容的同时仅在 `401` 确认与 `sourceKey`/身份绑定全部通过时记录内存见证。

## 2. 运输层双拒绝见证记录（`behavior_http.go`）

1. **私有见证结构**：在 `behaviorHTTPSecrets` 中维护：
   - `sourceKey []byte`：首次确认旧 `access` 拒绝时复制的有界源 JWT 密钥材料，后续同一运输层所有旧 `access` 确认必须与其字节相等（防止多用户之间源密钥漂移），并在 `Close()` 中 `clear()` 擦除。
   - `oldAccessWitness` 与 `oldRefreshWitness`（`map[uuid.UUID]oldTokenProbeWitness`）：仅在探测实际成功确认 `401`（及 `rejectOldRefresh` 前后数据库零副作用与控制快照稳定）后写入；`Close()` 时全部 `clear()`。
   - 每条 `oldTokenProbeWitness` 记录：`plan`、`consent`、`records [3]string`、`database`、`runtime`、`certificate`（TLS 叶证书 SHA-256）、`observedAt`、`tokenExpires`（旧 access JWT 的 `exp` 或旧 refresh 数据库 `expires_at`）与 `confirmed bool`。
2. **`confirmOldAccess(ctx, proof, user, currentVersion, access, sourceKey)`**：
   - 校验传输层未关闭、`proof` 与 `proof.guard` 有效且未关闭、`proof` 与 `t.binding`/`t.users` 对齐、时效在 `[proof.checked, proof.checked+20s]` 与 `proof.expires` 内、`sourceKey` 与目标密钥不同且与既有 `t.secrets.sourceKey` 一致，并在 `files[3]` 存在时核对 `stableHandoverSnapshot`。
   - 解析旧 `access` 的 `exp`（`checkOldBehaviorAccessAt` 已保证签名/用户/版本/未过期），随后调用 `t.rejectOldAccess(...)`。仅当返回 `nil` 且后置控制快照未变时，写入 `oldAccessWitness[user]`（`observedAt = t.clock()`、`tokenExpires = claims.ExpiresAt.Time`）。
   - 若同次 `proof.guard` 已存在 `behaviorRun`（即在 `dispatchBehaviorOnce` 之后调用），在释放 `t.mu` 后按 `guard.mu` 锁序将见证附加到 `behaviorRun`。
3. **`rejectOldRefresh(ctx, proof, user, refresh)`**：
   - 在通过 H1 的前置/后置数据库 `auth_sessions` 零变更与 `afterControl` 控制快照一致性核验后，写入 `oldRefreshWitness[user]`（`observedAt = t.clock()`、`tokenExpires = after.targetExpires`）；若同次 `proof.guard` 已存在 `behaviorRun`，按 `guard.mu` 锁序同步附加到 `behaviorRun`。

## 3. 运行见证捕获与同次附加（`behavior_dispatch.go` & `behavior_review.go`）

1. **`dispatchBehaviorOnce` 与 `captureConfirmedBehaviorRun`**：
   - `captureConfirmedBehaviorRun` 显式接收 `sourceKey` 与 `targetKey`，记录 `windowStart = a.intent.Issued`，并通过 `transport.oldTokenWitnessSnapshot(sourceKey, targetKey)` 复制运输层当前已确认的 `oldAccessWitness` 与 `oldRefreshWitness`。
   - 若运输层曾记录 `sourceKey` 或 `targetJWT` 但与本次 `dispatchBehaviorOnce` 的 `sourceKey`/`targetKey` 不一致，则将 `run.oldTokens.drifted = true`，永久阻止本运行将 `old_token_http_rejection` 置为 `pass`。
2. **后置登出后附加（`attachOldTokenWitness`）**：
   - 允许在 `dispatchBehaviorOnce` 之后、全部已知新会话确认 `Logout` 撤销后且 `transport.Close()` / `collectBoundBehaviorReview` 之前继续完成剩余白名单用户的旧令牌拒绝探测（如 H1 集成模式中 `users[1]` 的旧 `refresh_token` 探测）。
   - `attachOldTokenWitness` 在 `guard.mu` 下核验：`g.locations != nil`、`run != nil`、`!run.reviewStarted && run.reviewSHA == ""`、`!run.oldTokens.drifted`、`targetKey` 与 `run.targetJWT` 一致（若为 access 还要求 `sourceKey` 与 `run.sourceJWT` 一致）、`w.plan`/`w.consent`/`w.records`/`w.database`/`w.runtime`/`w.certificate` 与 `run` 完全一致且 `w.observedAt` 在 `[run.windowStart, run.expires)` 内；任何漂移立即将 `run.oldTokens.drifted = true` 并拒绝写入。

## 4. `collectBoundBehaviorReview` 与报告模型校验

1. **`boundBehaviorReview.validate()`**：
   - `Profile` 仍为 `"bound-https-account-verification-v1"`，`Status` 仍为 `"partial"`，`Execution`/`Ready`/`Deployment` 必须为 `false`，`Checks[0..9]` 必须为 `"pass"`，`Checks[11]`（`reader_roundtrip`）必须仍为 `"unavailable"`。
   - `Checks[10]`（`old_token_http_rejection`）仅允许 `"unavailable"` 或 `"pass"`（拒绝 `"fail"` 或未知状态）；`loadHandoverReview` 仍拒绝加载任何 `boundBehaviorReview`。
2. **`oldTokenRejectionConfirmed(p, run, s, a, actual, now)` 判定**：
   - 仅当 `!run.oldTokens.drifted`，且 `a.intent.Users` 中每一位白名单用户均同时拥有 `confirmed == true` 的 `access` 与 `refresh` 见证，且全部见证的 `plan`、`consent`、`records`、`database`、`runtime`、`certificate` 与当前运行/目标身份/控制快照完全一致，且 `observedAt` 在 `[a.intent.Issued, run.expires)` 内、`!observedAt.After(now)`、`now.Sub(observedAt) <= 5*time.Minute` 且 `now.Before(w.tokenExpires)` 时，才将 `old_token_http_rejection` 置为 `"pass"`。
   - 任一用户未执行、返回非 `401`、已超期或存在密钥/身份/快照漂移时，绝不将该项置为 `"pass"`。

## 5. 验证计划

1. `python scripts/verify-backup-contract.py`：65 项契约检查与 16 份冻结摘要不变。
2. `behavior_review_test.go` 与 `behavior_http_test.go` 单测：覆盖 `10 pass / 2 unavailable` 与 `11 pass / 1 unavailable` 报告模型、`loadHandoverReview` 拒绝、缺失任一用户/任一令牌类型、非 `401` 失败后未写入见证、源/目标 JWT 漂移、证书/计划/控制记录/运行身份漂移、超期（run 窗口超期与旧令牌自身 `exp`/`expires_at` 超期）、`attachOldTokenWitness` 边界与 `Close()` 擦除。
3. 运行 `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 与 `go vet -mod=readonly -tags=integration ./...`。
4. Linux 隔离容器联合验收（`scripts/verify-backup-restore-db.py --binary .toolchain/backup-old-token-witness-linux.test ... --behavior-review`）：`good` 目标中两位用户均通过 `confirmOldAccess` 与 `rejectOldRefresh`（含登录前与全部登出后），`bound-behavior-review.json` 输出 `11 pass / 1 unavailable`（`old_token_http_rejection = pass`，`reader_roundtrip = unavailable`，`status = partial`），`lost`/`redirect`/`wrong_pin` 无见证且拒绝生成报告。
