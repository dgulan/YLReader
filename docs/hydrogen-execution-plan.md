# Hydrogen 后续执行计划（备份恢复交接剩余工作包）

基线说明：基于 `docs/backup-next-stage-plan-2026-11-16.md` 与 `docs/backup-restore-stability-verification-2026-11-18.md`。执行期间必须遵守 `AGENTS.md`（净室隔离、契约先行、PowerShell UTF-8 编码规则），保持 43 个公开 API、`docs/schema.sql`、备份格式与 16 份冻结契约文件 SHA-256 摘要不变，不放宽恢复门禁或超时，不接触生产数据库。

---

## H1：旧 `refresh_token` 受限 HTTPS 拒绝探测与副作用隔离

- **任务编号**：`H1`
- **涉及文件**：
  - `docs/backup-old-refresh-design.md`（新增先行设计）
  - `backend/internal/backup/behavior_http.go`
  - `backend/internal/backup/behavior_http_test.go`
  - `backend/internal/backup/behavior_http_integration_test.go`
  - `docs/backup-old-refresh-verification.md`（新增验收记录）
- **契约要求**：
  - 仅在已核验的 `verifiedBehaviorAccounts` 用户白名单与源/目标数据库身份 pin 绑定下，通过固定 TLS 证书与禁代理/禁重定向/禁自动重试的受限 HTTPS 运输层向 `/api/v1/auth/refresh` 发起单次 `POST` 拒绝探测。
  - **执行顺序与副作用隔离**：鉴于服务端 refresh 重放检测可能触发同用户活跃会话撤销，旧 `refresh_token` 拒绝探测必须严格限定在目标新登录会话创建之前（或全部已知新会话确认 `Logout` 撤销之后）执行，并在探测前后通过只读事务核验目标库 `auth_sessions` 既有已撤销时间戳（`revoked_at`）未被篡改、未新增任何会话行。
  - 仅接受有界、未压缩、严格 JSON `Error` 结构的 `401` 响应；每位白名单用户最多尝试一次，传输错误或响应关闭失败一律视为未确认且禁止重发；`Close()` 擦除内存凭据与尝试状态。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup`
  - `go vet -mod=readonly -tags=integration ./...`
  - `python scripts/verify-backup-restore-db.py --binary .toolchain/backup-old-refresh-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`

---

## H2：同次旧令牌拒绝观察（Access + Refresh）并入 `bound-https-account-verification-v1` 见证

- **任务编号**：`H2`
- **涉及文件**：
  - `docs/backup-old-token-witness-design.md`（新增先行设计）
  - `backend/internal/backup/behavior_http.go`
  - `backend/internal/backup/behavior_dispatch.go`
  - `backend/internal/backup/behavior_review.go`
  - `backend/internal/backup/behavior_review_test.go`
  - `backend/internal/backup/behavior_http_integration_test.go`
  - `docs/backup-old-token-witness-verification.md`（新增验收记录）
- **契约要求**：
  - 将同一 `restoreGuard` 生命周期内全部白名单用户的旧 `access_token`（`GET /api/v1/auth/me` -> `401`）与旧 `refresh_token`（`POST /api/v1/auth/refresh` -> `401`）双拒绝确认纳入 `confirmedBehaviorRun` 内存见证（不可从 JSON 反序列化或从磁盘历史补造）。
  - 仅当全部白名单用户的旧 access 与旧 refresh 拒绝均在同一运行窗口、同一 TLS 证书 pin、同一源/目标 JWT 材料及控制记录快照下完成确认时，`collectBoundBehaviorReview` 才将 `bound-https-account-verification-v1` 报告中的 `old_token_http_rejection` 检查项置为 `pass`（此时报告为 `11 pass / 1 unavailable`，`reader_roundtrip` 仍为 `unavailable`，`status` 仍为 `partial`）。
  - 任何一位用户的旧 access 或旧 refresh 未执行、超期、返回非 `401` 或密钥/身份漂移时，拒绝生成该项 `pass`；禁止拼接旧 `isolated-http-fixture-v1` 报告结果。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup`
  - `go vet -mod=readonly -tags=integration ./...`
  - `python scripts/verify-backup-restore-db.py --binary .toolchain/backup-old-token-witness-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`

---

## H3：明确授权的受限 HTTPS 只读阅读行为回路（`reader_roundtrip`）与完整通用报告绑定

- **任务编号**：`H3`
- **涉及文件**：
  - `docs/backup-reader-roundtrip-design.md`（新增先行设计）
  - `backend/internal/backup/behavior_consent.go`
  - `backend/internal/backup/behavior_http.go`
  - `backend/internal/backup/behavior_dispatch.go`
  - `backend/internal/backup/behavior_review.go`
  - `backend/internal/backup/behavior_review_test.go`
  - `backend/internal/backup/behavior_http_integration_test.go`
  - `docs/backup-reader-roundtrip-verification.md`（新增验收记录）
- **契约要求**：
  - 仅在 `behaviorConsent` 显式授权只读阅读核验且处于同次受限 HTTPS 登录会话存活窗口（`Login` 确认后、`Logout` 撤销前）内，使用该次新签发的 `access_token` 对固定只读端点（`/api/v1/books`、`/api/v1/books/{id}/chapters`、本地章节 `/api/v1/books/{id}/content`、`/api/v1/books/{id}/progress`、`/api/v1/books/{id}/bookmarks` 及跨租户 `404` 隔离断言）执行有界只读请求。
  - 全程禁止触发外部书源网络抓取、禁止启动后台任务/队列、禁止修改用户阅读进度或书签数据；请求完成后必须立即执行 `Logout` 并通过独立只读事务确认会话 `revoked_at IS NOT NULL`、业务表行与对象树指纹零变更。
  - 当同次运行同时满足 10 项基础检查 + `old_token_http_rejection` + `reader_roundtrip` 共 12 项检查时，生成完整绑定的通用行为审阅报告（12 项 `pass`），但 `ready_for_cutover` 与 `deployment_authorized` 仍保持 `false`，不直接等同于生产流量切换。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup`
  - `go vet -mod=readonly -tags=integration ./...`
  - `python scripts/verify-backup-restore-db.py --binary .toolchain/backup-reader-roundtrip-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`

---

## H4：通用行为审阅报告的状态诊断展示与 CAS 登记衔接

- **任务编号**：`H4`
- **涉及文件**：
  - `docs/backup-bound-review-registration-design.md`（新增先行设计）
  - `backend/internal/backup/handover_registration.go`
  - `backend/internal/backup/status_report.go`
  - `backend/internal/backup/behavior_status_report.go`
  - `backend/cmd/backup/main.go`
  - `backend/cmd/backup/status_cli_linux_test.go`
  - `backend/internal/backup/handover_registration_test.go`
  - `docs/backup-bound-review-registration-verification.md`（新增验收记录）
- **契约要求**：
  - 扩展 `backup status` 与 `backup behavior-status` 只读诊断输出，使其能无歧义展示 `bound-behavior-review.json` 的存在性、完整/部分状态、待验项清单、过期/漂移冲突及与控制记录版本的绑定关系，绝不推断未确认的持久化状态。
  - 明确通用完整报告接入操作版本 CAS 登记（`handover-registration.json` + `state.json` generation 递增，phase 仍保持 `validating`）的门禁规则：部分报告（`status == "partial"` 或含 `unavailable`/`fail`）严禁进入 CAS 登记，确认丢失或冲突时保持只读保留且不可重放。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup`
  - `python scripts/verify-backup-status-cli.py`
  - `python scripts/verify-backup-behavior-status-cli.py`
  - `python scripts/verify-backup-restore-db.py --binary .toolchain/backup-bound-registration-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`

---

## H5：受控恢复执行 CLI 入口与操作员交接闭环

- **任务编号**：`H5`
- **涉及文件**：
  - `docs/backup-controlled-restore-cli-design.md`（新增先行设计）
  - `backend/cmd/backup/main.go`
  - `backend/cmd/backup/restore.go`（新增受控 CLI 入口）
  - `backend/cmd/backup/restore_cli_linux_test.go`（新增独立进程测试）
  - `scripts/verify-backup-restore-cli.py`（新增隔离容器 CLI 验证脚本）
  - `docs/backup-controlled-restore-cli-verification.md`（新增验收记录）
- **契约要求**：
  - 在 `backend/cmd/backup` 中严格区分只读预览命令（`inspect`、`plan`、`status`、`behavior-status`）与受控执行子命令；受控执行必须显式接收已冻结的计划摘要、目标实例确认 ID、操作员属主 UUID、安全目录句柄与超时上下文，绝不从 `inspect`/`plan` 预览自动派生执行权。
  - 复用 `restoreGuard` 全链门禁（空库排他复核、对象暂存、数据库恢复与 Header 核验、对象发布、交接审阅登记）；任何阶段出现错误、取消或 `ErrRestoreCommitUnknown` / `ErrPublication` / `errRegistrationUnknown` 时，严禁自动重试写入或删除保留对象，仅输出脱敏可核对状态码。
  - 不注册任何 Web Server 路由，不修改公开 `docs/openapi.yaml`（43 个操作），不自动启动常驻业务进程或切换生产流量。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup`
  - `go vet -mod=readonly -tags=integration ./...`
  - `python scripts/verify-backup-preview-cli.py`
  - `python scripts/verify-backup-restore-cli.py`
  - `python scripts/verify-backup-restore-db.py --binary .toolchain/backup-restore-cli-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`
