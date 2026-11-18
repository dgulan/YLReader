# 明确授权的受限 HTTPS 只读阅读行为回路与完整通用报告绑定验收（H3）

## 本轮范围

依据先行设计 [backup-reader-roundtrip-design.md](backup-reader-roundtrip-design.md)，在同次 `restoreGuard` 与受限 HTTPS 登录会话存活窗口内完成显式授权的只读阅读行为回路（`reader_roundtrip`），并在同次运行同时满足 10 项基础检查 + `old_token_http_rejection` + `reader_roundtrip` 时产出完整绑定的通用行为审阅报告（`status = "complete"`，12 项 `pass`），同时保持 `execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 均为 `false`：

- **`backend/internal/backup/behavior_consent.go`**：
  - 在 `behaviorConsent` 中新增可选字段 `ReaderScope`（JSON `reader_scope,omitempty`）与 `allowsReaderRoundtrip()`，仅允许空值或 `"readonly-local-reader-roundtrip"`，不破坏未授权只读阅读回路的既有同意文件。
- **`backend/internal/backup/behavior_http.go`**：
  - 在 `behaviorHTTPSecrets` 中新增私有不可序列化的 `readerTried`、`readerWitness`（`readerRoundtripWitness`）与 `readerDrifted` 状态，并在 `Close()` 中使用 `clear()` 擦除。
  - 抽取共用响应校验 helper `doJSON` 与 `verifyBehaviorError`（复用至 `unauthorized` 与 `not_found` 校验），新增严格受限白名单 `allowedReaderGETPath`、`get`、`checkUniqueJSONToken` 与 `decodeStrictReaderJSON`。
  - 新增 `observeReaderDB` 与 `readerRoundtrip(ctx, consent, proof, s)`：仅在 `behaviorConsent` 显式授权且会话存活（`Login` 确认后、`Logout` 撤销前）时执行；前置/后置通过独立 `REPEATABLE READ` + `READ ONLY` 事务（`accountObservationPolicy(..., false)`）核验 `jobs` 零活跃任务、本地书籍与可读章节、异租户书籍以及 12 张业务/会话表 SHA-256 快照前后零变更，其间使用新签发 `access_token` 对 6 个固定只读端点（`/api/v1/books`、`/api/v1/books/{id}/chapters`、本地章节 `/api/v1/books/{id}/content`、`/api/v1/books/{id}/progress`、`/api/v1/books/{id}/bookmarks` 与异租户 `/api/v1/books/{other}/content` `404`）执行有界只读校验并记录内存见证。
  - 新增 `readerWitnessSnapshot(consent, targetKey)`，在缺失授权或目标密钥漂移时标记 `drifted`。
- **`backend/internal/backup/behavior_dispatch.go`**：
  - 新增内部接口 `behaviorReaderTransport`，在 `dispatchBehaviorCore` 落盘 `session_observed` 后、执行 `transport.Logout` 前调用 `readerRoundtrip`；无论阅读回路成功、失败或传输层不支持，均保证立即执行 `Logout` 并记录 `logout.json`。
  - `dispatchBehaviorOnce` 的 `recheck` 同步核验 `g.locations.files[1]` 已发布对象目录树指纹仍等于 `s.pub.TreeFingerprint`，并在 `captureConfirmedBehaviorRun` 中捕获 `readerWitnessSnapshot`。
- **`backend/internal/backup/behavior_review.go`**：
  - 在 `confirmedBehaviorRun` 中新增私有 `reader confirmedReaderSet`，新增 `readerRoundtripConfirmed` 核验授权标记、逐用户与确切 `sessionID` 对齐、`beforeDigest == afterDigest`、异租户书籍 ID 差异、运行/身份/证书绑定及 5 分钟新鲜度窗口。
  - 更新 `boundBehaviorReview.validate()` 与 `collectBoundBehaviorReview`：全部 12 项均 `pass` 时 `status = "complete"`，任一待验项为 `unavailable` 时 `status = "partial"`；拒绝 `fail` 与状态错配；`execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 仍恒为 `false`，且 `loadHandoverReview` 仍拒绝加载。
- **测试与验证脚本配套更新**：
  - `backend/internal/backup/behavior_review_test.go`：更新 `TestBoundBehaviorReviewStrictPartialProfile` 覆盖 `10/2`、`11/1`（双方向）与 `12/0 complete` 模型及负向越权拒绝；新增 `TestReaderRoundtripConsentAndDispatchBoundary`、`TestReaderRoundtripWitnessValidation`（23 个子场景）与 `TestReaderRoundtripTransportBounded`（34 个子场景）。
  - `backend/internal/backup/behavior_http_integration_test.go`：在 `good` 场景为同意文件启用 `reader_scope = "readonly-local-reader-roundtrip"`，为 `postHandler` 装配完整只读服务依赖与 `headercrypto.SetDefaultKeyring(ring)`，注入 `forbiddenHTTPFetcher` 断言零外站抓取与 `12` 次受限只读 GET 请求；同时修正 `BEHAVIOR_HTTP_EVIDENCE` 记录实际 `loginRequests.Load()` / `logoutRequests.Load()`。
  - `backend/internal/backup/behavior_review_integration_test.go` 与 `scripts/verify-backup-restore-db.py`：同步断言 `bound-behavior-review` 为 `status = "complete"`、`12 pass / 0 unavailable`、`reader_requests = 12` 与 `external_fetches = 0`。

本轮未修改 43 个公开 API、`docs/schema.sql`、备份格式、16 份冻结契约摘要或旧 `isolated-http-fixture-v1` 报告及 CAS 登记。

## 验证结果

1. **契约检查**：`python scripts/verify-backup-contract.py` 通过（65 项契约检查通过，16 份冻结契约摘要匹配，报告 `.toolchain/backup-contract-checks.json`）。
2. **Windows 本机单测与静态检查**：
   - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 全通过（`internal/backup` 1.349s，`cmd/backup` 0.217s）。
   - `go vet -mod=readonly -tags=integration ./...`、`gofmt -s -l`、`python -m py_compile scripts/verify-backup-restore-db.py` 与 `git diff --check` 全部通过；未运行 `-race`。
3. **Linux 隔离 PostgreSQL 16.15 + Redis + 真实归档/TLS 联合验收**：
   - 复用命令：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-reader-roundtrip-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

   - 首次沙箱调用因 Docker 命名管道访问受限未开始（`.toolchain/backup-restore-db-0c14c52f175f/`）；随后使用授权宿主机 Docker 运行通过，最终证据目录 `.toolchain/backup-restore-db-67b9b687fced/`，`result.json` 为 `passed: true`、`passing_events: 127`、0 失败/跳过，4 个自有临时容器 `cleanup` 均为 `true`。
   - `bound-behavior-review.json` 实测：`profile = "bound-https-account-verification-v1"`、`scope = "localfs-bound-https-account-review"`、`status = "complete"`、12 项检查全部 `"pass"`（含 `old_token_http_rejection = "pass"` 与 `reader_roundtrip = "pass"`），`execution_authorized` / `ready_for_cutover` / `deployment_authorized` 均为 `false`；`good` 模式下 `reader_requests = 12`、`external_fetches = 0`、`old_access_requests = 2`、`old_refresh_requests = 2`、`active_after_dispatch = 0`；`lost` / `redirect` / `wrong_pin` 均为 `reader_requests = 0` 且拒绝生成报告。

| 证据项 | SHA-256 / 值 |
|---|---|
| Linux 测试二进制（`.toolchain/backup-reader-roundtrip-linux.test`） | `92301a430eae7cb9e25f33e85b8e721054cdbf394ac9129bc3d6a380498b96d9` |
| 合成数据库归档 | `3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303` |
| PostgreSQL 16.15 镜像 | `sha256:f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657` |
| Redis 镜像 | `sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b` |

## 进入 H4 前的边界说明

- `bound-behavior-review` 现已覆盖同次双旧令牌拒绝与受限只读阅读回路并达到完整 12 项 `pass`（`status = "complete"`），但 `execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 仍恒为 `false`，不直接等同于生产流量切换。
- `backup status` 与 `backup behavior-status` 尚未展示 `behavior-account-review.json` 的存在性、完整/部分状态与待验项清单，且通用完整报告尚未接入操作版本 CAS 登记门禁（留待 H4）及受控恢复执行 CLI 入口（留待 H5）。
- 本轮未接触 `references\` 或任何生产数据库，未执行 git commit/reset/checkout。
