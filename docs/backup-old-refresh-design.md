# 旧 refresh_token 受限 HTTPS 拒绝探测与副作用隔离设计

状态：H1 内部先行设计，不修改 43 个公开 API、`docs/schema.sql`、备份归档格式、16 份冻结契约摘要、派发账本或通用审阅报告 profile（`bound-behavior-review` 在本阶段仍保持 `partial` / `10 pass, 2 unavailable`，不接入 `old_token_http_rejection` 或 CAS 登记）。

## 1. 目标与边界

为 `behaviorHTTPTransport` 增加显式内部方法 `rejectOldRefresh(ctx, proof, user, refresh)`，在受限本机 HTTPS 运输层下验证恢复目标 API 对已撤销旧 `refresh_token` 的 `401` 拒绝行为，同时严格隔离服务端 refresh 重放检测对活跃会话的副作用。

- 不自动接入 `dispatchBehaviorOnce`，不写 `behavior-actions` 账本，不生成 `confirmedBehaviorRun` 见证或执行许可。
- 不传入密码或旧 `access_token`，不在请求中携带 `Authorization`、`Cookie`、`GetBody` 或幂等重试头。
- 错误文本仅返回脱敏哨兵 `errBehaviorHTTP`，不泄露明文 `refresh_token`、DSN、响应正文或底层传输错误。

## 2. 身份、白名单与控制记录绑定

调用 `rejectOldRefresh` 前必须满足以下全部前置绑定（任一项不满足即拒绝，不发送网络请求）：

1. **传输层与上下文有效**：`ctx` 非空且未取消；`t` 非空且未 `Close()`；`databasePinsSeparate(t.source, t.target)` 成立。
2. **`verifiedBehaviorAccounts` 绑定**：
   - `proof` 与 `proof.guard` 非空，且 `proof.guard` 未关闭（`locations != nil`），`proof.guard.plan.Digest == proof.plan`、`proof.guard.plan.TargetID == t.binding.TargetID`。
   - `proof.database == t.binding.Database`、`proof.runtime == t.binding.Runtime`。
   - 时效窗口有效：`validStamp(now)` 且 `now` 在 `[proof.checked, proof.checked+20s]` 与 `proof.expires` 之前。
   - 用户白名单集合严格对齐：`proof.users` 非空、无重复、无 `uuid.Nil`，集合与 `t.users` 完全一致，且目标 `user` 属于该白名单。
   - 若控制目录读句柄存在（`files[3] != nil`），调用 `stableHandoverSnapshot` 确认控制记录摘要仍与 `proof.records` 一致。
3. **凭据与单次尝试约束**：`refresh` 必须为 64 位小写十六进制字符串（`validSHA`）；`t.secrets.oldRefreshTried[user]` 必须为 `false`。

## 3. 执行顺序与数据库副作用隔离

鉴于 `service.AuthService.Refresh` 检测到已撤销 `refresh_token` 时会执行 `UPDATE auth_sessions SET revoked_at = now() WHERE user_id=$1 AND revoked_at IS NULL`：

1. **运输层时序门禁**：仅允许在以下两种时序下执行 `rejectOldRefresh`：
   - 目标新登录会话创建之前：`len(t.secrets.loginTried) == 0 && len(t.secrets.known) == 0`。
   - 全部已发起的新登录会话均已确认 `Logout` 撤销之后：`len(t.secrets.known) == 0`，且每个 `t.secrets.loginTried[uid] == true` 的用户均在 `t.secrets.completed` 中拥有已确认撤销的会话记录（从而阻断在活跃会话期间或 `login_response_unknown` / `logout_response_unknown` 失联窗口内执行探测）。
2. **探测前/后只读事务核验**：
   - 复用目标库 `REPEATABLE READ` + `READ ONLY` 事务与 `accountObservationPolicy(..., false)`（核验源/目标数据库 pin 分离、目标系统/库/角色 OID、非特权属主、`server_version_num` 16.x 与 `schema_migrations` 冻结账本，仅放宽因 API 服务合法持有连接池所需的排他连接项）。
   - 确认目标用户在 `public.users` 中存在且 `is_disabled = false`。
   - 以 `sha256(refresh)` 查询 `public.auth_sessions`：必须存在且归属 `user`、`revoked_at IS NOT NULL`、且 `expires_at > clock()`（确保是被撤销拒绝而非自然过期）。
   - 确认 `public.auth_sessions` 当前无任何活跃会话（目标用户 `activeForUser == 0` 且全表 `revoked == total`）。
   - 按 `id` 顺序遍历 `public.auth_sessions`（`SELECT id, user_id, revoked_at`），计算包含每行 `(id, user_id, revoked_atPresence, revoked_at.UTC().UnixNano())` 的 SHA-256 摘要及 `(total, revoked)` 计数。
   - 探测完成后立即开启新的 `REPEATABLE READ` + `READ ONLY` 事务重算上述快照：必须满足 `total`、`revoked` 与全表摘要完全一致、`targetExpires > clock()`、`activeForUser == 0` 且 `revoked == total`。任何行数新增/删除或既有 `revoked_at` 时间戳变化一律判为未确认。

## 4. 受限 HTTPS 运输与内存擦除

1. 通过本地身份/白名单/时序前置核验后、进入探测前只读核验与网络请求前即锁存 `t.secrets.oldRefreshTried[user] = true`；即使后续发生只读核验失败、连接中断、超时、重定向、非 `401`、响应体关闭失败或后置数据库核验失败，也禁止再次对同一 `user` 发起 `/api/v1/auth/refresh` 请求。
2. 复用 `behaviorHTTPTransport` 固定叶证书 SHA-256 pin、IP SAN 与证书时效校验、禁代理/cookie/压缩/HTTP2/keep-alive/重定向客户端；`t.post` 白名单扩展允许 `/api/v1/auth/refresh` 仅匹配 `401` 状态、无 `Authorization` 头且响应体上限 `8192` 字节。
3. 响应必须为 `401 Unauthorized`、`Content-Type: application/json`、无压缩（`Content-Encoding` 为空或 `identity`）、`<= 8192` 字节、严格 UTF-8、无重复/未知 JSON 键且符合冻结 `Error` 结构（`error.code == "unauthorized"`、非空 `error.message`、可选非 `null` `request_id`/`details`）。
4. 序列化请求体与读取的响应体在函数退出时使用 `clear()` 擦除；`Close()` 清除 `oldRefreshTried`、密钥与会话状态并关闭空闲连接。

## 5. 验证计划

1. **契约检查**：`python scripts/verify-backup-contract.py` 保持 65 项检查与 16 份冻结摘要全部通过。
2. **本机单测与静态检查**：`behavior_http_test.go` 覆盖合法 `401`、身份/pin/白名单/时效/guard 关闭拒绝、活跃会话与失联会话时序拒绝、前置/后置 `auth_sessions` 篡改与新增行检测、单次尝试不可重放、非 `401`/重定向/压缩/超限/重复键/错误结构/网络及关闭失败与 `Close()` 擦除；运行 `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 与 `go vet -mod=readonly -tags=integration ./...`。
3. **Linux 隔离联合集成验收**：在 `behavior_http_integration_test.go` 的 `good` 目标中分别覆盖新登录创建前（`users[0]`）与全部已知新会话确认 `Logout` 撤销后（`users[1]`）的真实 TLS `POST /api/v1/auth/refresh` 拒绝探测、禁止重放及 `auth_sessions` 行数与 `revoked_at` 不变；运行完整 `scripts/verify-backup-restore-db.py` 联合链。
