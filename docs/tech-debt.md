# 技术债务与契约待办（Tech Debt & Contract Backlog）

本文件归档「实现已落地、但契约或实现存在已知边界」的条目，供 v1.3 契约修订与后续压测阶段
统一处理。**只记录，不在此处修改 `docs/openapi.yaml` / `docs/schema.sql`。**

> v1.3（Stage 4 M3c）终审：7 项 DEF-OAS 契约待办已随 `docs/openapi.yaml` Frozen v1.3 闭环；
> 6 项 DEF-CODE 中 4 项闭死（03/05/06/07），2 项归档（CODE-04/CODE-08）。

## 索引

| 编号 | 级别 | 描述 | M3c 终审状态 |
|---|---|---|---|
| DEF-OAS-01 | P2 契约 | PUT /replace-rules 缺 400 声明 | 已闭环 (v1.3) |
| DEF-OAS-02 | P2 契约 | PATCH /settings 缺 400 声明 | 已闭环 (v1.3) |
| DEF-OAS-03 | P2 契约 | importLocalBook 自动重命名未声明 | 已闭环 (v1.3) |
| DEF-OAS-04 | P2 契约 | ErrorCode enum 缺少通用 422 错误码 | 已闭环 (v1.3) |
| DEF-OAS-05 | P2 契约 | cacheBook 的 Idempotency-Key 未做强幂等 | 已闭环 (v1.3) |
| DEF-OAS-06 | P2 契约 | cancelJob 对 queued/running 任务的取消时序未细化 | 已闭环 (v1.3) |
| DEF-OAS-07 | P2 契约 | ErrUnprocessable 返回 validation_error 的契约外行为 | 已闭环 (v1.3) |
| DEF-OAS-08 | P2 契约 | CacheBookRequest.source_id 暂不支持多源缓存预热 | 已闭环 (v1.4) |
| DEF-CODE-03 | P2 实现 | maxNameAuthorAttempts 上限对 10+ 并发不足 | 已闭环 (上限由 5 提升至 20) |
| DEF-CODE-04 | P2 测试 | handleCommitError 状态 ③ 无法单测 | 归档（M3c 未处理，待后续架构重构时处理） |
| DEF-CODE-05 | P3 测试 | 预检查命中 + 3 并发重试边界与命名验证 | 已闭环 |
| DEF-CODE-06 | P3 并发 | POST /cache 单书防重存在理论竞态 | 已闭环 (Advisory Lock 显式事务) |
| DEF-CODE-07 | P2 部署 | MarkStaleRunningAsFailed 缺少进程标识隔离 | 已闭环 (Worker 租约 + 同机/跨机/历史任务隔离单测) |
| DEF-CODE-08 | P2 部署 | MarkStaleRunningAsFailed 跨节点超时任务缺常驻后台巡检 | 已闭环 (v1.4) |
| DEF-SEC-01 | P1 安全 | 书源 Header 敏感凭据缺乏应用层 AES-GCM 信封加密 | 已闭环 (v1.4) |
| DEF-CODE-09 | P2 架构 | chapter_cache 物理表自 M2c 起未被写入，正文缓存全量落在缓存抽象层 | 归档（v1.5 架构评估是否保留物理表） |
| DEF-BEHAVIOR-01 | P3 行为 | handleCommitError 三态语义变更记录 | 已记录 |
| DEF-CODE-10 | P2 构建 | 本机无 C 工具链，go test -race 不可用（自 M1c 起） | 归档（CI/Docker 环境执行 race 检测） |
| DEF-CODE-11 | P2 查询优化 | relocate_fuzzy 未使用 % 前置索引谓词，pg_trgm GIN 无法加速函数调用 | 归档（v1.5 契约修订） |

---

### DEF-OAS-01（P2 契约）：PUT /replace-rules 缺 400 声明

**发现于**：M3b-1 替换规则更新校验

**问题**：`docs/openapi.yaml` 中 `updateReplaceRule` 仅声明 200 与 404。

**当前实现**：非法正则返回 400 `validation_error`。

**建议 v1.3**：`updateReplaceRule` 增加 `'400': { $ref: '#/components/responses/BadRequest' }`。

**v1.3 处置**：已在 `docs/openapi.yaml` 的 `updateReplaceRule.responses` 补充 `'400'` 引用，契约冻结 v1.3，闭环。

---

### DEF-OAS-02（P2 契约）：PATCH /settings 缺 400 声明

**发现于**：M3b-1 用户设置增量更新校验

**问题**：`docs/openapi.yaml` 中 `patchSettings` 仅声明 200。

**当前实现**：非法 key 返回 400 `validation_error`。

**建议 v1.3**：`patchSettings` 增加 `'400': { $ref: '#/components/responses/BadRequest' }`。

**v1.3 处置**：已补充 `'400'` 引用，闭环。

---

### DEF-OAS-03（P2 契约）：importLocalBook 自动重命名未声明

**发现于**：M3b-1 并发同名书场景修复

**问题**：`docs/openapi.yaml` 的 `importLocalBook` 只声明了「显式 title 冲突返回 409」，
未声明「非显式 title 的同名书导入会自动追加 `(n)` 后缀」这一行为。实现已按此语义
（顺序导入 M3a 已实现、并发导入 M3b-1 已修复），但契约缺显式描述。

**当前实现**（已被 M3a + M3b-1 测试覆盖）：

- 顺序导入同名书：第二本自动重命名为 "书名 (1)"
- 并发导入同名书：重试循环内逐次重命名，至多 20 次（v1.3 由 5 提升），均返回 201
- 重试耗尽：返回 409 `conflict`（`ErrDuplicate`）

**建议 v1.3 契约补充**：

在 `importLocalBook` 的 description 中追加：

> 当 title 参数省略或为空时，若与同用户已有书籍同名同作者，服务端自动追加 `(n)`
> 后缀，返回 201 而非 409；仅当显式传入 title 且冲突时返回 409。

**v1.3 处置**：已在 `importLocalBook.description` 追加上述契约级语义，闭环。

---

### DEF-OAS-04（P2 契约）：ErrorCode enum 缺少通用 422 错误码

**问题**：`docs/openapi.yaml` 的 `Error.code` enum 中无通用 422 语义码（如 `unprocessable_entity`）。422 响应场景（如 `addMapping` 书源不可用、`cacheBook` 本地书拦截）只能复用 `rule_execution_failed` 或 `validation_error`。
**建议 v1.3**：在 `Error.code` 补充 `unprocessable_entity` 枚举值。

**v1.3 处置**：`Error.code` 已新增 `unprocessable_entity`；`oapi-codegen` 生成 `ErrorCodeUnprocessableEntity`；`mapErrorToCode(ErrUnprocessable)` 改为 `422 + unprocessable_entity`，`messageForCode` 同步补齐文案。闭环。

---

### DEF-OAS-05（P2 契约）：cacheBook 的 Idempotency-Key 未做强幂等

**问题**：`POST /books/{bookId}/cache` 声明了 `Idempotency-Key` 头，但本批次仅解析不使用；
防重依赖 `HasActiveFullCache`（同书同类型 queued/running 409），而非按 key 去重。

**建议 v1.3**：明确「同一 Idempotency-Key 在 24h 内重复请求返回同一 Job」或将该头标注为可选未启用。

**v1.3 处置**：契约已明确「校验格式（8-128 字符）并记录链路日志；当前批次不做强幂等去重，防重依赖单书在途任务拦截」。闭环（语义澄清，非功能新增）。

---

### DEF-OAS-06（P2 契约）：cancelJob 对 queued/running 任务的取消时序未细化

**问题**：契约声明 `DELETE /jobs/{jobId}` 响应 204，但未细分 queued 与 running 的取消时序。
**当前实现**：queued 任务在 DB 中直接置为 canceled；running 任务通过 Context 协同中断。
**建议 v1.3**：在 `cancelJob` description 中明确 queued 与 running 的取消时序，并说明「running 任务由 Worker 在下一个检查点退出，已完成的章节保持缓存」。

**v1.3 处置**：`cancelJob.description` 已按上述语义补齐，闭环。

---

### DEF-OAS-07（P2 契约）：ErrUnprocessable 返回 validation_error 的契约外行为

**问题**：`ErrUnprocessable` 映射为 `(422, "validation_error")`。虽然 `validation_error` 存在于全局 enum 中，但规范上对应 400 状态码。严格遵循 OpenAPI Schema 校验的客户端可能会提示错误码与状态码不符。
**关联引用**：本条目是 DEF-OAS-04 的实现衍生问题；若 v1.3 在 `Error.code` 补充 `unprocessable_entity` 枚举并同步调整实现，则本条目与 DEF-OAS-04 同时关闭。
**建议 v1.3**：统一收敛错误码与状态码的绑定规则。

**v1.3 处置**：随 DEF-OAS-04 同步关闭——`ErrUnprocessable` 现返回 `422 + unprocessable_entity`，错误码与状态码严格绑定。闭环。

---

### DEF-OAS-08（P2 契约）：CacheBookRequest.source_id 暂不支持多源缓存预热

**问题**：`POST /books/{bookId}/cache` 接受 `source_id`，但 v1.3 实现只预热 `books.active_source_id` 目录；显式传入非活跃源返回 422。契约此前未声明该约束，客户端可能误以为可指定任意已启用源。
**v1.3 处置**：`docs/openapi.yaml` 在 `CacheBookRequest.source_id` 与 `cacheBook` description 显式声明「显式 source_id 必须等于活跃源，否则 422」，并标注多源预热未支持；实现侧三层门禁（无活跃源 / 活跃源禁用 / 显式源不一致）与契约一致。
**v1.4（S5-2）处置**：改为多源预热——显式 `source_id` 只要属于该书 `book_source_mappings` 且目标源存在可读章节即可预热；只写 `chapter_cache(book_id, target_source_id, chapter_index)`，不改 `books.active_source_id` 与 `chapters`；锁粒度改为 `cache:<book_id>:<source_id>`；Job 响应新增 `target_source_id`（由 payload 解析，不落库）。契约同步：`CacheBookRequest.source_id` 描述、`Job.target_source_id`、`cacheBook` 202/409/422 触发条件、architecture §4.6.7 + ADR-011、schema jobs 注释。
**覆盖用例**：原 `TestM3b2CacheSourceMismatchRejected` 更名为 `TestM3b2CacheSourceNotInMappingsRejected`；新增多源预热、活跃源不变、无章节拒绝、同源防重、活跃源缺 mapping 容忍、同书跨源并发等用例。
**当前状态**：已闭环 (v1.4)。

---

### DEF-CODE-03（P2 实现）：maxNameAuthorAttempts 上限对 10+ 并发不足

**发现于**：M3b-1 并发同名书修复

**问题**：`ImportService.maxNameAuthorAttempts = 5`。假设 10 本同名书并发导入，
最坏情况第 10 本需 9 次重命名重试才能落库，5 次上限会耗尽，返回 409。

**v1.3 处置**：上限提升至 **20**（`backend/internal/service/import.go`），并新增
`TestM3b1Concurrent10NameAuthorConflict`：10 路同名同作者并发导入 → 全部 201、
库中严格 10 本（`十重同名书`、`(1)`~`(9)`）、无嵌套畸形名，实测 **549.9ms**（门禁 ≤ 2500ms）。闭环。

**历史实测（5 次上限时期）**：

- 2 本并发：`[201 201]`，145.8ms（单用例）
- 3 本并发：`[201 201 201]`，0.76s（含测试夹具开销）
- `TestNextAvailableNameExhausted`：候选名占满 (1)~(99) 后确定返回 `ErrDuplicate`

**残留风险**：>20 路同名并发仍可能耗尽；小团队单用户场景可接受。

---

### DEF-CODE-04（P2 测试）：handleCommitError 状态 ③ 无法单测

**问题**：`probeCtx` 为 `handleCommitError` 内部创建的 `context.Background()+5s`，无法从外部注入失败条件；`s.pool` 为共享测试池，Close 会污染其他用例。

**优化建议**：M3c 将 `ImportService.pool` 抽象为窄接口（含 `Begin`、`QueryRow`），允许测试注入"Get 返回非 ErrNotFound 错误"的 fake。

**当前接受度**：状态 ③ 分支逻辑简单（保守不删），代码 review 已确认，无测试可接受。**M3c 未处理，归档**；`TestHandleCommitError_ProbeFails` 保持 `t.Skip` 并注明本条编号。

---

### DEF-CODE-05（P3 测试）：预检查命中 + 3 并发重试边界与命名验证

**发现于**：M3b-2 第一步（P2-1 断言收紧）

**场景**：先同步导入 1 本「基准书/作者甲」（显式 title + author），再并发发起 3 个同名「基准书」、
无显式 title/author 的 TXT 导入（文件字节不同 → file_hash 不同）。

**实测**（`TestM3b2ConcurrentSameNamePreallocatedID`，v1.3 复测 131.1ms）：

- 3 个并发请求全部 `201`（statuses=[201 201 201]）
- DB 中 `name LIKE '基准书%'` 的书籍严格 4 本：`基准书`（作者甲）、`基准书`（空作者）、`基准书 (1)`（空作者）、`基准书 (2)`（空作者）
- 无嵌套括号（`基准书%(%(%` 命中 0），证明 `nextAvailableName` 按「去掉既有 (n) 后缀再递增」实现

**关于「两本同名」的说明**：唯一键是 `(user_id, normalized_name, normalized_author)`；基准书作者为
`作者甲`，并发 TXT 无内嵌作者（空串），因此「基准书/作者甲」与「基准书/空作者」是两条合法记录。
重命名递增只发生在 `(基准书, 空作者)` 组内，故为 `(1)`、`(2)` 而非 `(3)`。

**结论**：K≤20 的重命名重试在 3 并发下完全收敛；与 DEF-CODE-03 的上限风险一致。

**规范备注**：本条属测试边界验证、非关键阻塞缺陷，级别定为 P3；随 M3b-2 封板闭环。

---

### DEF-CODE-06（P3 并发）：POST /cache 单书防重存在理论竞态

**问题**：`cacheBook` 的 409 防重拦截原采用"先查后插"策略。若微秒级并发请求同时到达，双方在未提交事务中均查不到对方，可能插入两条重复的 `full_cache` 任务。
**实际影响**：人类交互与前端防抖下几乎不触发；单 Worker 串行执行时亦可天然吸收。
**建议 M3c**：采用 `pg_advisory_xact_lock` 或局部表达式唯一索引防御。

**v1.3 处置**：已改为显式事务 + `pg_advisory_xact_lock(hashtextextended('cache:' || book_id, 0))`
64 位排他咨询锁，查重与入队在同一事务内完成，前置无锁查重已彻底移除。闭环。

---

### DEF-CODE-07（P2 部署）：MarkStaleRunningAsFailed 缺少进程标识隔离

**问题**：`WorkerPool.Start` 启动时无条件将库中所有 `status = 'running'` 的任务更新为 `failed`。在单实例部署（当前场景）下能自愈异常关机遗留任务；但在多 Pod / 多进程集群部署下，新实例启动会误将其他活跃实例正在执行的任务篡改为 `failed`。
**建议 M3c**：在 `jobs` 表增加 `worker_id`（主机名+PID）或心跳检测机制（`worker_heartbeats` 表），避免全局暴力更新。

**v1.3 处置**：`jobs.worker_id` 物理列 + `ix_jobs_worker_status` 部分索引已落地；`WorkerPool` 生成
`hostname-<pid>-<nano>` 实例标识并在 `claimOne` 中登记；`MarkStaleRunningAsFailed` 改为三准则
（同机旧实例 / 跨机超 2 小时 / 历史 NULL）。`TestWorkerPoolMultiInstanceIsolation` 断言
跨机活跃任务保持 `running`、同机旧实例与历史 NULL 立即 `failed`。闭环。

---

### DEF-CODE-08（P2 部署）：MarkStaleRunningAsFailed 跨节点超时任务缺常驻后台巡检

**问题**：M3c 演进后，`MarkStaleRunningAsFailed` 仅在 `WorkerPool.Start` 时触发一次。若远程节点发生宕机，且当前健康节点长期常驻运行（未重启），该远程残留的 running 任务必须等待当前节点重启时才能回收。
**建议后续演进**：引入后台巡检 Goroutine 或在 Worker 主循环中周期性调度跨机超时回收。
**当前状态**：已闭环 (v1.4)。

**v1.4（S5-3）处置**：`WorkerPool.reaperLoop` 启动同步初检 + 每 10min Ticker
周期执行 `MarkStaleRunningAsFailed`，脱离仅依赖节点重启的局限；全部 worker
协程统一迁移至派生 ctx，`Shutdown` 双通道等待无死锁，`goleak` 覆盖无泄漏。
**白名单理由登记**：`goleak` 若需 `IgnoreTopFunction`，白名单为 pgxpool 后台健康检查协程
（`(*Pool).triggerHealthCheck`，以 `go test -tags integration ./internal/service/ -v` 实测堆栈函数名为准），
该协程在测试周期内随连接池常驻，非本次 WorkerPool 协程泄漏。

---


### DEF-SEC-01（P1 安全）：书源 Header 敏感凭据缺乏应用层 AES-GCM 信封加密

**问题**：当前书源 `header` 中的 Cookie/token 等敏感凭证以 JSONB 明文存储在数据库中，未做应用层加密防护。
**处置决议**：Stage 4 已正式封板，该项安全缺口不回退代码返工，正式转入 Stage 5 首项任务（作为 v1.4 契约演进的首要特性实现）。
**当前状态**：已闭环 (v1.4)。
**实现**：`pkg/crypto` AES-256-GCM 信封（nonce 12B 随机、AAD=`user_id|source_id`、多 kid 并存轮换、未知 kid/篡改/AAD 不匹配一律显式报错）；`SourceRepository` 写前密封、读后透明解密（历史明文兼容，密文损坏→500 不回退明文）；`SourceService.Create` 预生成 id（AAD 需要）；`cmd/migrate-header` 按 id 游标分批迁移（1000 行/批、幂等、可断点续跑、`-dry-run`）；导入 `dry_run=true` 不密封不落库。
**测试**：`TestEnvelopeRoundTrip`/`TestEnvelopeTamperRejected`/`TestEnvelopeKidRotation`/`TestEnvelopeAADMismatchRejected`/`TestKeyringValidation`、`TestSourceHeaderEncryptedAtRest`/`TestSourceHeaderLegacyPlaintextReadable`/`TestSourceHeaderUnknownKIDFailsLoudly`/`TestMigrateSourceHeadersIdempotent`/`TestMigrateSourceHeadersDryRunAndResume`、`TestSourceHeaderTransparentViaAPI`/`TestSourceImportDryRunDoesNotSeal`/`TestSourceImportNonDryRunSeals`。
**交付口径（S5-1）**：
- 业务层交付口径：90 PASS + 2 SKIP（repo 25 + api 58 + service 7）；
- 底层密码学组件包 pkg/crypto 贡献 5 PASS（含 TestKeyringValidation 构造校验 1 条）；
- 四层合计 95 PASS + 2 SKIP；全工程 140 PASS + 2 SKIP；
- pkg/crypto 从 S5-2 起正式计入交付基线；S5-1 交付口径不含 pkg/crypto。
- 终审补交（提交 `22b56b1` 代码+测试 / `fbc9d55` 文档口径）：新增 `TestSourceImportNonDryRunSeals`（+1）→ 全工程 141 PASS + 2 SKIP；业务层 91 PASS + 2 SKIP；四层合计 96 PASS + 2 SKIP。另 P1 加固：`sealSourceHeaderValue` 删除「已是信封即跳过」捷径（伪造 `{"__enc":"x"}` 不再原样落库）、`openSourceHeader` 对非 string `__enc` 显式 `ErrMalformed`、`ImportExportRepository.updateSource` 显式密封。
- Export 读路径闭环（本轮，提交号见 `git log -1 --format="%h %s"` 实际输出）：`ImportExportRepository.Export` 的 SELECT 追加 `id, user_id`、Scan 后显式 `openSourceHeader`，`GET /sources/export` 不再吐出 `__enc` 密文；新增 `TestSourceExportTransparent`（body 无 `__enc` + 解析后 `header.Cookie` 明文，+1 → 全工程 142 PASS + 2 SKIP，业务层 92，四层 97）。同收 P2：`openSourceHeader` 对非 object header（如 `[1,2,3]`）显式 `ErrMalformed`，新增 `TestSourceHeaderNonObjectRejected`（不计入下限，实测为准）。
- 转入 S5-2 首周（本轮显式登记，不视为遗漏）：`TestSourceImportOverwriteSeals`（`updateSource` 已有加密代码但缺独立断言）、`Import` 中 `err == pgx.ErrNoRows` 改为 `errors.Is(err, pgx.ErrNoRows)`。

---

### DEF-CODE-09（P2 架构）：chapter_cache 物理表自 M2c 起未被写入

**问题**：`chapter_cache` 物理表（schema §6）自 M2c 起未被任何代码写入；正文缓存全量落在缓存抽象层（`yreader:cache:content:<book>:<source>:<index>`，Redis 或内存），与 `architecture.md` §4.6.7 早期的「只写 `chapter_cache` 表」表述不一致。

**处置决议（S5-2）**：保持高性能 Redis/内存缓存架构，不增加冗余 DB 表写入；`architecture.md` §4.6.7 已按实际改为「写入缓存键 `yreader:cache:content:<book>:<source>:<index>`」；物理表 `chapter_cache` 保留但不再写入，v1.5 架构评估是否删除该表或重新引入对象存储层缓存。

**当前状态**：归档（v1.5 架构评估是否保留物理表）。

**交叉引用**：`architecture.md` §4.6.7 已同步为「正文缓存键包含 source_id」的表述；物理表 `chapter_cache` 保留但不写入。

---

### DEF-BEHAVIOR-01（P3 行为）：handleCommitError 三态语义变更记录

**变更记录**：
- M3a 原行为：Commit 错误时永远保留文件（含明确 ErrNotFound 场景）
- M3b-1 新行为：`ErrCommitFailed` 失败→探查行是否存在；存在→保留；`ErrNotFound`→清理；探查失败→保留并记录 `probe_err`

**变更理由**：ErrNotFound 表示事务确实未提交，清理安全；避免孤儿文件无限堆积。

**风险评估**：`pgxpool.Pool` 每 Query 新事务（READ COMMITTED），ErrNotFound 即"DB 明确无此行"，无"已提交但看不到"的窗口。

**回归验证**：`TestHandleCommitError_RetainOnRowExists`、`TestHandleCommitError_CleanupOnRowAbsent` 覆盖；状态 ③ 见 DEF-CODE-04。

---

### DEF-CODE-10（P2 构建）：本机无 C 工具链，go test -race 不可用

**问题**：`go test -race` 需要 cgo 与 C 编译器；本机探测 gcc/clang/zig/cc 均缺失，执行即报 `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`（自 M1c 起持续存在）。
**替代证据**：涉及并发的批次以「非 race 模式连续两次全量 PASS + 静态锁审查（禁止 SELECT-then-UPDATE、sync.Mutex 仅保护 Start/Shutdown 状态）」替代。
**后续处置**：CI 流水线在 Linux 镜像内设置 `CGO_ENABLED=1` 执行 `go test -race`，作为合并门禁；本地不阻塞。
**当前状态**：归档（CI/Docker 环境执行 race 检测）。

---

### DEF-CODE-11（P2 查询优化）：生产换源 relocate_fuzzy 未使用 % 前置索引谓词

**问题**：`docs/schema.sql` §9 `relocate_fuzzy` 函数当前仅写 `similarity(normalized_title, p_normalized_title) >= p_threshold`，未附带 `%` 运算符；`pg_trgm` GIN 索引无法加速函数调用，10k+ 章节的大目录换源 Level 2 存在全表扫描退化风险。
**建议 v1.5**：函数体内改为 `WHERE ... AND normalized_title % p_normalized_title AND similarity(...) >= p_threshold`，前置 `%` 谓词让 GIN 生效。
**修复归属**：需修改 `docs/schema.sql` `relocate_fuzzy` 函数定义，属 v1.5 契约变更流程（三份文件 + ADR），不在 S5-4 范围。
**当前状态**：归档（v1.5 契约修订）
**S5-4 证据边界**：`TestChaptersTitleTrigramExplain` 使用 `SET LOCAL pg_trgm.similarity_threshold = 0.85` 收敛到 10 条目标行，确定性证明 `ix_chapters_title_trgm` 可服务 `%` 谓词；该结果**不等于**生产 `relocate_fuzzy` 在 0.6 阈值下的查询计划——0.6 阈值下候选集更大，GIN cost 上升，规划器仍可能选 `(book_id, source_id)` btree 位图；且生产函数当前未加 `%` 前置谓词，GIN 无法被调用。v1.5 修复后需以生产实际阈值 0.6 重新 EXPLAIN 断言，方可宣告 GIN 生效。
---

## Stage 4 封板记录（M3c 结项）

- 封板日期：2026-11-10；契约版本：`Frozen v1.3`（openapi / schema / architecture 三份一致），operationId 43 个全唯一。
- 回归基线：85 项 = 83 PASS + 2 SKIP（`TestContentCacheRedisKeyAndTTL` 需 `REDIS_ADDR`；`TestHandleCommitError_ProbeFails` 见 DEF-CODE-04）。
- 终审收口：P3-1（failJob 独立上下文）已于 M3c 修复闭环——`failJob` 移除 `ctx.Err()` 短路并统一 `context.Background()`，Shutdown 窗口内 `ListByBookSourceRange` 失败也能可靠落终态。
- 转入后续版本统一管理的归档项：P3-2（优雅停机瞬态中间进度更新可能丢失，不影响终态语义）、DEF-CODE-04（状态 ③ 无法单测）、DEF-OAS-08（多源缓存预热）。DEF-SEC-01 归档流转至 Stage 5（v1.4，书源 header AES-GCM 信封加密）；DEF-CODE-08（跨节点常驻巡检）已由 S5-3（v1.4）闭环：WorkerPool 启动同步初检 + 每 10min Ticker 巡检 + 统一派生 ctx 双通道 Shutdown。
- 流程教训（Stage 4）：封板期两次出现「草稿推演被标注为原始输出」（56 条测试清单、144a74f stat 22/10）。Stage 5 凭证规范：标注「原始输出」的段落必须可复现；禁止用整理版/统计版冒充原始版；凡与上轮输出矛盾，当轮撤回并声明，不得两版并存。
- 43 operationId 清单核验：依据 Frozen v1.3 `docs/openapi.yaml`（基线 `89f9b18`，`144a74f` 未改动该文件），按 path 前缀分组逐行统计：auth 4 / sources 8 / search 1 / books 6 / chapters 1 / chapter-override 2 / content 1 / progress 2 / switch-source 1 / mappings 3 / bookmarks 3 / replace-rules 4 / jobs 3 / settings 2 / health 2 = 43，重复 0。
- 前序收官令措辞收窄归档：§三「85 项集成回归零衰退闭环」→「83 PASS + 2 SKIP（合计 85 项，0 FAIL）」；§二 第 1 项「224 行注释成功移除干扰字样」→「224 行注释措辞规整，非逻辑修复，diff 仅 +1/-1」。
- 本封板令为 Stage 4 最终法定记录；后续变更须走 Stage 5 或 v1.4 契约修订流程。
- Stage 5 S5-2（DEF-OAS-08 多源缓存预热）闭环：契约四份同步（openapi / architecture §4.6.7 + ADR-011 / schema jobs 注释 / 本台账），实现改为按「书 + 目标源」独立锁与独立缓存目录；代码提交 `86aff0f`、契约提交 `42b70bb`；实测 **151 PASS + 2 SKIP + 0 FAIL**（较 S5-1 的 143 + 8）；`TestM3b2CacheSourceMismatchRejected` 更名为 `TestM3b2CacheSourceNotInMappingsRejected`；S5-1 首周三项同收（`TestSourceImportOverwriteSeals` / `errors.Is(pgx.ErrNoRows)` / `?ids=` 精准导出）。
- 不采纳项（记录在案）：上轮审计 P2-3「`TestCacheMultiSourcePreheat` 仅断言 `f.count() != 0`」——`succeeded` 断言已隐含每章成功抓取，且 `res.total` 断言（3 vs 4 章不同）覆盖了源区分；若 S5-4 压测发现覆盖率缺口再补。
- 本批新登记 DEF-CODE-09（`chapter_cache` 物理表自 M2c 起未被写入 → 归档 v1.5 架构评估）。
- 单章响应式预热（非任务型，如 `GET /content?prefetch=true`）推迟到 v1.5 设计；S5-2 只交付 Worker 预热路径 `WarmChapterForSource`。

---

## Stage 5 封板记录（v1.4 结项）

- 封板日期：2026-11-10；契约版本：`Frozen v1.4`（openapi / schema / architecture 三份一致），`info.version` 保持 `1.0.0`；公开 `operationId` 仍为 43 个（`/metrics` 与 `/debug/pprof` 挂载于顶层路由，不纳入 OpenAPI）。
- 各批提交（代码 / 契约文档分离）：
  - S5-1（DEF-SEC-01 书源 header AES-GCM 信封）：`3033c16` / `52af42f`，后续 import 与 export 明文路径补齐 `22b56b1` / `fbc9d55`、`d49d998` / `1bdde31`。
  - S5-2（DEF-OAS-08 多源缓存预热）：`86aff0f` / `42b70bb`，收口纠偏 `3a3fc18` / `4dc1363`。
  - S5-3（DEF-CODE-08 跨节点租约常驻巡检）：`0674470` / `c36df85`。
  - S5-4（压测、观测收口与本封板）：代码提交 `bdc8336`、契约文档提交「本提交（Stage 5 封板文档）」。
- 测试跃迁轨迹（集成用例级，`go test -tags integration ./... -count=1 -v`）：Stage 4 基线 85 → S5-1 143 → S5-2 151 → S5-3 155 → S5-4 **160 PASS + 2 SKIP + 0 FAIL**。
- 43 operationId 零破坏核验：S5-4 仅新增顶层 `/metrics` 与条件挂载的 `/debug/pprof/*`，`docs/openapi.yaml` 未新增任何 path，`operationId:` 计数实测 43。
- S5-4 观测与压测收口：`backend/internal/api/metrics.go` 注册 `yreader_jobs_total` / `yreader_sandbox_executions_total` / `yreader_ssrf_blocked_total` 并预置零值 series；`/metrics` 免 JWT（反代限内网），`/debug/pprof/*` 由 `PPROF_ENABLED=true` 开启；10k 种子 EXPLAIN 断言 `ix_book_sources_rules_gin`（`rules @>`）与 `ix_chapters_title_trgm`（`%` 前置谓词）均可被选用且不出现 `Seq Scan`。
- 遗留至 v1.5 的归档项：DEF-CODE-04（状态 ③ 无法单测）、DEF-CODE-09（`chapter_cache` 物理表未写入）、DEF-CODE-10（本机无 C 工具链，`-race` 改由 CI/Docker 执行）、DEF-CODE-11（`relocate_fuzzy` 未使用 `%` 前置索引谓词）。
- 本批未触发 DEF-CODE-12：api 包 goleak `TestMain` 一次通过，仅白名单 `pgxpool.(*Pool).triggerHealthCheck`（纯字符串，无 Markdown 链接）。
- S5-4 的 trigram EXPLAIN 为索引可选用性验证，非生产 0.6 阈值下查询计划验证。
- 两 EXPLAIN 用例在 `enable_seqscan=off` 下断言索引可服务性；生产默认配置下规划器是否主动选索引，以 v1.5 压测实测为准。