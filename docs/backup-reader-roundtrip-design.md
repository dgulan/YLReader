# 明确授权的受限 HTTPS 只读阅读行为回路与完整通用报告绑定设计（H3）

状态：H3 内部先行设计，不修改 43 个公开 API、`docs/schema.sql`、备份归档格式、16 份冻结契约摘要或旧 `isolated-http-fixture-v1` 报告；即使 `bound-behavior-review` 达到 12 项全 `pass`（`status = "complete"`），`execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 仍恒为 `false`，且暂不接入 CAS 登记（留待 H4）。

## 1. 目标与边界

在 `behaviorConsent` 显式授权只读阅读核验且处于同次受限 HTTPS 登录会话存活窗口（`Login` 确认并落盘 `response.json` 后、`Logout` 撤销前）内，使用该次新签发的 `access_token` 对恢复目标 API 执行有界只读阅读回路（`reader_roundtrip`），并在同次运行同时满足 10 项基础检查 + `old_token_http_rejection` + `reader_roundtrip` 共 12 项检查时，输出完整绑定的通用行为审阅报告（`status = "complete"`，12 项 `pass`）。

- 全程禁止触发外部书源网络抓取、禁止启动后台任务/队列、禁止修改用户阅读进度、书签、书籍或会话表行。
- 无论 `reader_roundtrip` 成功或失败，必须在同次迭代立即执行 `Logout` 撤销当前会话并确认数据库 `revoked_at IS NOT NULL`，不残留未撤销会话。
- 未显式授权 `reader_scope` 时，`reader_roundtrip` 不执行且报告保持 `partial`（`reader_roundtrip = "unavailable"`），与既有 H1/H2 行为完全兼容。

## 2. 显式授权扩展（`behavior_consent.go`）

1. 在 `behaviorConsent` 增加可选字段 `ReaderScope string`（JSON 键 `reader_scope,omitempty`）：
   - 未提供时保持空字符串，表示仅授权既有账号登录/登出与旧令牌检查，不授权只读阅读请求（`allowsReaderRoundtrip() == false`）。
   - 提供时必须在 `exactJSON` 中满足非空字符串、无重复/未知键，且 `validate()` 仅允许 `"readonly-local-reader-roundtrip"`，否则返回 `errBehaviorConsent`。
2. `prepareBehaviorInput`、`recordBehaviorIntent` 与 `dispatchBehaviorOnce` 均复用 `consent.validate()` 与 `consent.matches(...)`，确保同意范围与控制记录、计划摘要、运行配置修订摘要及 5 分钟有效期强绑定。

## 3. 受限 HTTPS 只读阅读回路（`behavior_http.go` & `behavior_dispatch.go`）

1. **运输层状态与单次尝试锁存（`behaviorHTTPSecrets`）**：
   - 增加 `readerTried map[uuid.UUID]bool`、`readerWitness map[uuid.UUID]readerRoundtripWitness` 与 `readerDrifted bool`，并在 `Close()` 中使用 `clear()` 全部擦除。
   - `readerRoundtripWitness` 为私有不可序列化结构，记录 `plan`、`consent`、`records`、`database`、`runtime`、`certificate`、`sessionID`、`bookID`、`otherBookID`、`observedAt`、`beforeDigest`、`afterDigest` 与 `confirmed`。
2. **会话存活窗口执行约束（`dispatchBehaviorCore`）**：
   - 在 `persistBehaviorAction(..., "session_observed")` 成功确认且 `ctx.Err() == nil && check() == nil` 后、调用 `transport.Logout(...)` 前，若 `prepared.consent.allowsReaderRoundtrip()` 为 `true`：
     - 要求 `transport` 实现内部 `behaviorReaderTransport` 接口；若未实现，视为未确认失败，仍立即执行 `Logout` 并返回 `errBehaviorDispatch`。
     - 调用 `readerRoundtrip(ctx, prepared.consent, proof, response)`（使用受取消控制的 `ctx` 与有界子超时，不使用 `context.Background()`）；无论成功或失败，随后均必须执行 `transport.Logout(cleanup, response)` 并记录 `logout.json`。
   - `dispatchBehaviorOnce` 的 `recheck` 同步核验 `g.locations.files[1]` 中已发布对象目录树指纹仍与 `s.pub.TreeFingerprint` 一致。
3. **只读数据库前置/后置核验（`observeReaderDB`）**：
   - 复用 `REPEATABLE READ` + `READ ONLY` 事务及 `accountObservationPolicy(..., false)`：
     - 核验 `users.is_disabled = false`、当前 `sessionID` 在 `auth_sessions` 中处于未撤销且未过期状态、`public.jobs` 中无 `queued`/`running` 任务。
     - 查询该用户至少一本 `book_type = 'local'` 且 `active_source_id IS NOT NULL` 的本地书籍，以及至少一章 `is_volume = false AND chapter_index IS NOT NULL` 的本地可读章节（确保 `/chapters` 不会触发 `TOCService.refresh`）。
     - 查询库中属于其他 `user_id` 的真实异租户书籍 `otherBookID`（无真实异租户书籍时拒绝伪造 404 检查），以及当前 `reading_progress` 与 `bookmarks` 预期值。
     - 对 `users`、`auth_sessions`、`books`、`chapters`、`reading_progress`、`bookmarks`、`book_sources`、`book_source_mappings`、`chapter_source_overrides`、`replace_rules`、`jobs`、`app_settings` 按主键排序生成确定性 SHA-256 业务表快照摘要。
4. **固定 6 个只读端点 HTTPS 请求（`t.get`）**：
   - 复用固定叶证书 SHA-256 pin、Literal IP 拨号、禁代理/Cookie/压缩/HTTP2/keep-alive/重定向/重试的运输层，要求 `Authorization: Bearer <new_access_token>`、`Content-Type: application/json`、`Content-Encoding` 为空或 `identity`、严格 UTF-8 与无重复/未知键 JSON：
     1. `GET /api/v1/books` -> `200` `api.BookPage`，包含目标 `bookID` 且 `book_type == "local"`。
     2. `GET /api/v1/books/{bookID}/chapters` -> `200` `api.ChapterList`，不带 `refresh`/`source_id`，包含目标 `chapterIndex`。
     3. `GET /api/v1/books/{bookID}/content?chapter_index={chapterIndex}` -> `200` `api.ChapterContent`，强制 `cached == false`、`trace_id == null`、正文非空、`word_count > 0`。
     4. `GET /api/v1/books/{bookID}/progress` -> `200` `api.Progress`，与只读库快照一致。
     5. `GET /api/v1/books/{bookID}/bookmarks` -> `200` `[]api.Bookmark`，数量与逐条字段与只读库快照一致。
     6. `GET /api/v1/books/{otherBookID}/content?chapter_index={otherChapterIndex}` -> `404` 严格 `Error` 对象（`error.code == "not_found"`，非空 `error.message`）。
   - 请求完成后立即开启独立 `REPEATABLE READ` + `READ ONLY` 事务复算快照，要求 `after.digest == before.digest`、`activeJobs == 0`、`sessionID` 仍活跃，方才写入 `readerWitness[user]`。

## 4. 完整通用报告绑定与校验（`behavior_review.go`）

1. **`confirmedBehaviorRun` 见证捕获**：
   - `captureConfirmedBehaviorRun` 同时调用 `transport.readerWitnessSnapshot(prepared.consent, targetJWT)` 将 `confirmedReaderSet` 写入私有 `confirmedBehaviorRun.reader`；若 `targetJWT` 漂移或运输层标记 `readerDrifted`，永久阻止 `reader_roundtrip` 置为 `pass`。
2. **`readerRoundtripConfirmed(p, run, s, a, actual, now)`**：
   - 校验 `run.reader.authorized && !run.reader.drifted`、所有白名单用户与确切 `run.sessions` 一一对应、`confirmed == true`、`beforeDigest == afterDigest`（有效 SHA-256）、`bookID != otherBookID`、绑定 `plan`/`consent`/`records`/`database`/`runtime`/`certificate` 完全一致，且 `observedAt` 在 `[a.intent.Issued, run.expires)` 与 `now` 5 分钟新鲜度窗口内。
3. **`boundBehaviorReview.validate()` 完整与部分状态门禁**：
   - `Checks[0..9]` 必须为 `"pass"`；`Checks[10]`（`old_token_http_rejection`）与 `Checks[11]`（`reader_roundtrip`）仅允许 `"pass"` 或 `"unavailable"`（拒绝 `"fail"`）。
   - `Status` 仅允许 `"partial"` 或 `"complete"`：当且仅当全部 12 项均为 `"pass"` 时 `Status == "complete"`；只要存在任一 `"unavailable"` 则 `Status == "partial"`。
   - 无论 `partial` 或 `complete`，`execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 必须恒为 `false`；`loadHandoverReview` 仍拒绝加载任何 `boundBehaviorReview`。

## 5. 验证计划

1. `python scripts/verify-backup-contract.py`：65 项契约检查与 16 份冻结契约摘要不变。
2. `behavior_review_test.go` 单测：覆盖 `10/2`、`11/1`（含两种单项 pass）与 `12/0 complete` 报告模型及负向边界（状态与 checks 错配、授权标志越权、`fail` 状态、`loadHandoverReview` 拒绝）、`readerRoundtripConfirmed` 全场景、运输层 `readerRoundtrip` 6 端点与 DB 前后零变更/未授权/失活会话/重放/密钥漂移测试、以及 `dispatchBehaviorCore` 在 `readerRoundtrip` 失败或不支持时强制 `Logout` 清理。
3. 运行 `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 与 `go vet -mod=readonly -tags=integration ./...`。
4. Linux 隔离容器联合验收（`scripts/verify-backup-restore-db.py --binary .toolchain/backup-reader-roundtrip-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`）：`good` 目标完成全部双旧令牌拒绝与受限只读阅读回路，`bound-behavior-review.json` 产出 `profile = "bound-https-account-verification-v1"`、`status = "complete"`、`12 pass / 0 unavailable` 且三个切换授权标记仍为 `false`；`lost`/`redirect`/`wrong_pin` 零阅读请求且拒绝生成报告。
