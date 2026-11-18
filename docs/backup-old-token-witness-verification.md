# 同次旧令牌拒绝观察（Access + Refresh）并入通用行为见证验收（H2）

## 本轮范围

依据先行设计 [backup-old-token-witness-design.md](backup-old-token-witness-design.md)，将同一 `restoreGuard` 生命周期内的旧 `access_token`（`GET /api/v1/auth/me` -> `401`）与旧 `refresh_token`（`POST /api/v1/auth/refresh` -> `401`）双拒绝确认并入 `confirmedBehaviorRun` 内存见证，并由 `collectBoundBehaviorReview` 严格校验后将 `bound-https-account-verification-v1` 报告的 `old_token_http_rejection` 置为 `pass`（`11 pass / 1 unavailable`、`reader_roundtrip` 仍为 `unavailable`、`status` 仍为 `partial`）：

- **`backend/internal/backup/behavior_http.go`**：
  - 在 `behaviorHTTPSecrets` 中增加私有不可序列化的 `oldTokenProbeWitness` 映射（`oldAccessWitness`、`oldRefreshWitness`）、有界 `sourceKey` 副本与 `oldTokenDrifted` 标记，并在 `Close()` 中使用 `clear()` 擦除。
  - 新增 `confirmOldAccess(ctx, proof, user, currentVersion, access, sourceKey)`，绑定 `verifiedBehaviorAccounts`、`restoreGuard`、`sourceKey` 一致性与前后 `stableHandoverSnapshot`，解析 UTC `tokenExpires`（修复 JWT `NumericDate` 默认本地时区导致的 `validStamp` 失败）并在 `rejectOldAccess` 实际确认 `401` 后记录见证。
  - 更新 `rejectOldRefresh`，在通过前后数据库零副作用与控制快照核验后记录 `oldRefreshWitness`（UTC `tokenExpires`），并支持与 `confirmOldAccess` 共同通过 `attachOldTokenWitness` 在 `dispatchBehaviorOnce` 之后、登出撤销后将确认见证附加至同一 `restoreGuard` 的 `behaviorRun`。
  - 新增 `oldTokenWitnessSnapshot(sourceKey, targetKey)` 在 `dispatchBehaviorOnce` 捕获时校验源/目标 JWT 材料一致。
- **`backend/internal/backup/behavior_dispatch.go` & `backend/internal/backup/behavior_review.go`**：
  - `captureConfirmedBehaviorRun` 显式接收 `sourceKey` 与 `targetKey` 并捕获初始 `oldTokens` 与 `windowStart = a.intent.Issued`。
  - 新增 `attachOldTokenWitness` 与 `oldTokenRejectionConfirmed`：要求全部白名单用户的旧 access 与旧 refresh 均在同一运行窗口、同一 TLS 证书 pin、同一源/目标 JWT 材料、同一控制记录快照下确认且均未超期（含运行窗口与令牌自身 `tokenExpires`），任何漂移、缺漏、非 `401` 或超期一律阻止 `old_token_http_rejection` 升为 `pass`。
  - `boundBehaviorReview.validate()` 仍强制 `status == "partial"`、`execution_authorized`/`ready_for_cutover`/`deployment_authorized` 均为 `false`、`reader_roundtrip` 必须为 `unavailable`，仅允许 `old_token_http_rejection` 为 `unavailable` 或 `pass`。
- **测试与验证脚本配套更新**：
  - `backend/internal/backup/behavior_review_test.go`：校验 `10/2` 与 `11/1` 报告模型及 `loadHandoverReview` 拒绝，新增 `TestOldTokenRejectionWitnessValidation`（18 个子场景）与 `TestOldTokenTransportWitnessCaptureAndAttach`。
  - `backend/internal/backup/behavior_http_integration_test.go`：在 `good` 场景使用 `confirmOldAccess` 绑定两位用户的旧 access 拒绝，配合新登录前（`users[0]`）与登出后（`users[1]`）的 `rejectOldRefresh`。
  - `backend/internal/backup/behavior_review_integration_test.go` 与 `scripts/verify-backup-restore-db.py`：新增 `SourceJWT` 漂移负向断言，并将 `bound-behavior-review` 预期从 `10 pass / 2 unavailable` 同步更新为 `11 pass / 1 unavailable`。

本轮未修改 43 个公开 API、`docs/schema.sql`、备份格式、16 份冻结契约摘要或旧 `isolated-http-fixture-v1` 报告及 CAS 登记。

## 验证结果

1. **契约检查**：`python scripts/verify-backup-contract.py` 通过（65 项契约检查通过，16 份冻结契约摘要匹配，报告 `.toolchain/backup-contract-checks.json`）。
2. **Windows 本机单测与静态检查**：
   - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 全通过（`internal/backup` 4.025s，`cmd/backup` 0.158s）。
   - `go vet -mod=readonly -tags=integration ./...`、`gofmt -s -l`、`python -m py_compile scripts/verify-backup-restore-db.py` 与 `git diff --check` 全部通过；未运行 `-race`。
3. **Linux 隔离 PostgreSQL 16.15 + Redis + 真实归档/TLS 联合验收**：
   - 复用命令：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-old-token-witness-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

   - 首次沙箱调用因 Docker 命名管道访问受限未开始（`.toolchain/backup-restore-db-ba536b9e6f8b/`）；随后使用授权宿主机 Docker 运行通过，最终证据目录 `.toolchain/backup-restore-db-c3f49f38474f/`，`result.json` 为 `passed: true`、`passing_events: 127`、0 失败/跳过，4 个自有临时容器 `cleanup` 均为 `true`。
   - `bound-behavior-review.json` 实测：`profile = "bound-https-account-verification-v1"`、`status = "partial"`、`verified_checks = 11`、`unavailable_checks = 1`（`old_token_http_rejection = "pass"`、`reader_roundtrip = "unavailable"`），`execution_authorized` / `ready_for_cutover` / `deployment_authorized` 均为 `false`；`lost` / `redirect` / `wrong_pin` 无见证且均拒绝生成报告。

| 证据项 | SHA-256 / 值 |
|---|---|
| Linux 测试二进制（`.toolchain/backup-old-token-witness-linux.test`） | `ea601dc8efbe00fcd2017909cf3cae8783f28a7fde8d9cdaab02d28810ccf2a9` |
| 合成数据库归档 | `3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303` |
| PostgreSQL 16.15 镜像 | `sha256:f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657` |
| Redis 镜像 | `sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b` |

## 进入 H3 前的边界说明

- `bound-behavior-review` 现为 `11 pass / 1 unavailable`（`status = "partial"`），`reader_roundtrip` 仍为 `unavailable`，尚未接入受限 HTTPS 只读阅读行为回路及完整 `12 pass` 报告（待 H3）。
- 通用报告尚无 `backup status` / `backup behavior-status` 诊断展示及 CAS 登记门禁（待 H4），亦无受控恢复执行 CLI 入口（待 H5）。
- 本轮未接触 `references\` 或任何生产数据库，未执行 git commit/reset/checkout。
