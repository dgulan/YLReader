# YLReader v1.0.0 正式发布执行计划（R1–R6 原子工作包）

基线说明：当前分支 `codex/pre-release-20261113`（`HEAD = 74c23ccb`），灾备恢复子系统（`backend/internal/backup` 与 `backend/cmd/backup`）已独立完成验收签发。本计划针对全仓其余模块制定 **R1–R6 六个顺序原子工作包**，供 Hydrogen 逐步执行以达成 v1.0.0 公开正式发布条件。

### 全局红线与不变量
1. **宪章与编码红线**：严格遵守 `AGENTS.md`（净室隔离原则，严禁读取/复制 `references\` 下任何代码；前端双层 UI 策略，阅读器核心视图严禁引入 Element Plus 组件；Windows PowerShell 命令必须显式前置 UTF-8 编码声明）。
2. **契约与冻结摘要零破坏**：保持 43 个公开 OpenAPI `operationId`、`docs/openapi.yaml`、`docs/schema.sql`、`docs/architecture.md`、`docs/research/*.md` 及 6 份独立备份协议共 **16 份冻结文件的 SHA-256 摘要完全不变**（每步必须通过 `python scripts/verify-backup-contract.py`）。所有行为差异均通过修正实现向冻结契约对齐，严禁篡改已冻结契约或触碰 `backend/internal/backup`、`backend/cmd/backup`。
3. **测试数据库门禁**：严禁连接或写入任何持久生产数据库；本地与 CI 数据库操作必须通过 `python scripts/check-db-target.py` 静态门禁并仅指向 `yreader_test` 或 `yreader_a1_iso_*`。

---

## R1：后端契约对齐与书源引擎能力补齐（Contract Alignment & Source Engine Parity）

- **任务编号**：`R1`
- **涉及文件**：
  - `backend/internal/service/overrides.go`
  - `backend/internal/api/handlers_overrides.go`
  - `backend/internal/api/m3b1_test.go`（或新增 `backend/internal/api/overrides_preview_test.go`）
  - `backend/internal/service/source_header.go`（新增受控静态 Header 解析与清洗辅助）
  - `backend/internal/service/content.go`
  - `backend/internal/service/toc.go`
  - `backend/internal/service/debug.go`
  - `backend/internal/service/search.go`
  - `backend/internal/service/source_header_outbound_test.go`（新增）
  - `backend/internal/engine/sandbox/bridge.go`
  - `backend/internal/engine/sandbox/sandbox_test.go`
  - `docs/r1-engine-parity-verification.md`（新增验收记录）
- **契约与安全要求**：
  1. **`setChapterOverride` 试抓正文与缓存刷新对齐（`openapi.yaml` §`POST /books/{bookId}/chapters/{chapterIndex}/override`）**：
     - 在 `OverrideService.Set`（注入 `ContentService` 或抓取/解析依赖）中，完成目标书源匹配与归属校验后，必须对 `chapter_url` 执行真实 SSRF 预检与正文试抓（复用 `ContentService` 10s 预算、分页完整性校验与替换净化管线）。
     - 试抓或规则解析失败时直接返回对应契约错误码（`403 blocked_private_address`、`422 rule_execution_failed`/`content_unavailable`、`502 source_request_failed`、`504 source_timeout`），且**不落库**。
     - 试抓成功后在同一事务内 Upsert `chapter_source_overrides`，刷新 `yreader:cache:content:<book_id>:<source_id>:<chapter_index>` 缓存，并按 Unicode code point 截取试抓正文前 512 字符填充响应 `ChapterOverride.content_preview`（非空）。
  2. **书源静态 `src.Header` 安全出网贯通**：
     - 从已透明解密的 `BookSource.Header`（JSON Object）解析静态请求头，并接入 `ContentService`、`TOCService`、`DebugService` 与 `SearchService`（`SearchService` 中 `searchUrl` 显式请求头优先级高于 `src.Header` 同名键）。
     - **安全清洗与防注入**：严格拒绝含 CR/LF/NUL 控制字符的键或值；仅放行安全出网头白名单（`User-Agent`、`Accept`、`Accept-Language`、`Referer`、`Origin`、`Cookie`、`Authorization` 及 `X-*` 扩展头），禁止覆盖 `Host`、`Content-Length`、`Transfer-Encoding`、`Connection`、`Proxy-*`；保留 `Fetcher.checkRedirect` 跨 host 重定向自动剥离 `Authorization`/`Cookie` 的安全边界；若配置了独立 `sandboxworker`（`java.ajax`/`get`/`post`），不得在日志、Trace 或错误消息中泄露任何 Header 敏感值。
  3. **JS 沙箱 `java.aesEncode` / `java.aesDecode` 扩展模式兼容**：
     - 保持现有两参数 `java.aesEncode(data, key)` / `java.aesDecode(data, key)`（AES-128-ECB + MD5(key) + PKCS7 + Base64）100% 向后兼容。
     - 扩展支持 Legado 常用三/四参数调用签名 `(data, key, transformation, iv)` 及 `java.aesBase64DecodeToString` / `java.aesDecodeToString` 别名：支持 `AES/CBC/PKCS5Padding`（及 `PKCS7Padding`）与 `AES/ECB/PKCS5Padding`，支持原始 16/24/32 字节 UTF-8 key（不足或非标准长度回退 MD5 16 字节）与 16 字节 IV，输入大小受沙箱 `InputLimit` 约束，异常参数或非法填充一律安全返回空串 `""` 而不触发未捕获 panic。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `cd backend && go vet -mod=readonly -buildvcs=false -tags=integration ./...`
  - `cd backend && go test -mod=readonly -buildvcs=false -count=1 ./internal/engine/sandbox ./internal/engine/fetcher ./internal/service ./internal/api`

---

## R2：后台任务扩展与书源健康度指标闭环（Background Jobs & Source Health Metrics）

- **任务编号**：`R2`
- **涉及文件**：
  - `backend/internal/repository/source_health.go`（新增 `source_failures` 与 `book_sources` 健康度原子更新仓储）
  - `backend/internal/repository/source_health_test.go`（新增）
  - `backend/internal/service/source_health.go`（新增成功/失败归因与熔断记录服务）
  - `backend/internal/service/jobs.go`
  - `backend/internal/service/worker_test.go`
  - `backend/internal/service/search.go`
  - `backend/internal/service/toc.go`
  - `backend/internal/service/content.go`
  - `docs/r2-jobs-health-verification.md`（新增验收记录）
- **契约与安全要求**：
  1. **`book_sources` 健康度指标与 `source_failures` 落地（对齐 `docs/schema.sql` §3）**：
     - 在 `SearchService.searchOne`、`TOCService.refresh`、`ContentService.fetchAndClean` 的真实回源抓取完成后（忽略 `context.Canceled` 客户端主动取消），异步或有界超时（`<= 500ms`，失败仅记日志不阻断主请求）更新 `book_sources`：
       - 成功：`failure_count = 0`、`last_checked_at = now()`、`last_success_at = now()`，并按指数滑动平均更新 `respond_time`（ms，非负整数）。
       - 失败：`failure_count = failure_count + 1`、`last_checked_at = now()`，同时向 `source_failures` 表写入一条归因记录（`kind` 严格映射枚举 `'network' | 'timeout' | 'rule' | 'blocked' | 'unsupported' | 'auth'`，`expires_at > occurred_at` 默认 `+10m`，`message` 必须脱敏、不含目标完整 URL 凭据或响应原文）。
  2. **`JobService.ExecuteJob` 多任务类型分发（对齐 `docs/schema.sql` `job_type` 枚举）**：
     - 保持 `POST /books/{bookId}/cache`（`full_cache`）现有行为、Advisory Lock 与 `target_source_id` 语义完全不变。
     - 在 `JobService` 与 `WorkerPool` 中补齐内部可调度的 `toc_refresh`（调用 `TOCService` 刷新指定 `(user_id, book_id, source_id)` 目录并落终态结果）与 `source_health`（对指定书源执行有界探活并更新 `book_sources` / `source_failures`）执行器，以及 `chapter_prefetch`（相邻可读章节缓存预热）执行分支，并同步更新 `yreader_jobs_total` Prometheus 指标计数。
     - 任何未知或载荷畸形的 `job_type` 必须以独立 `context.Background()` 标记 `failed`（`internal_error`），严禁滞留 `running`。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `cd backend && go vet -mod=readonly -buildvcs=false -tags=integration ./...`
  - `cd backend && go test -mod=readonly -buildvcs=false -count=1 ./internal/repository ./internal/service ./internal/api`

---

## R3：前端冻结契约全覆盖（Frontend API Parity）

- **任务编号**：`R3`
- **涉及文件**：
  - `frontend/src/api/reader.ts`
  - `frontend/src/api/client.test.ts`
  - `frontend/src/views/admin/AdminView.vue`
  - `frontend/src/views/admin/sourceManage.ts`（新增单源新建与导出逻辑）
  - `frontend/src/views/admin/sourceManage.test.ts`（新增）
  - `frontend/src/views/admin/SettingsView.vue`
  - `frontend/src/views/bookshelf/BookEditorView.vue`
  - `frontend/src/views/bookshelf/bookEditor.ts`
  - `frontend/src/views/bookshelf/bookEditor.test.ts`
  - `frontend/src/views/reader/ReaderView.vue`
  - `frontend/src/views/reader/ChapterOverrideDialog.vue`（新增原生 `<dialog>` 单章覆盖组件）
  - `frontend/src/views/reader/chapterOverride.test.ts`（新增）
- **契约与安全要求**：
  1. **补齐 `frontend/src/api/reader.ts` 全部缺失的冻结 API 调用封装**：
     - `me`: `GET /auth/me`
     - `createSource`: `POST /sources`
     - `exportSources`: `GET /sources/export`（支持可选 `ids` 查询参数）
     - `addMapping`: `POST /books/{bookId}/mappings`
     - `deleteMapping`: `DELETE /books/{bookId}/mappings/{mappingId}`
     - `setChapterOverride`: `POST /books/{bookId}/chapters/{chapterIndex}/override`
     - `deleteChapterOverride`: `DELETE /books/{bookId}/chapters/{chapterIndex}/override`
  2. **管理与书架视图补齐（Element Plus 层）**：
     - `AdminView.vue`：新增“新建书源”表单抽屉/面板（调用 `POST /sources`，校验 `http://` 或 `https://` URL 与 JSON Object 格式的 `rules`/`header`）与“导出全部/选中书源”按钮（调用 `GET /sources/export` 并触发浏览器 `.json` 下载）。
     - `BookEditorView.vue`：针对在线书籍展示已绑定的备用书源列表（`GET /books/{bookId}/mappings`），支持手动追加备用源映射（`POST /books/{bookId}/mappings`）与移除非活跃源映射（`DELETE /books/{bookId}/mappings/{mappingId}`，活跃源按钮禁用并提示先换源）。
     - `SettingsView.vue`：顶部新增“当前账户信息”卡片，通过 `GET /auth/me` 读取并展示用户名、显示名、角色、令牌版本与最近登录时间（遵守会话代次与 owner 校验，退出或切号后丢弃晚到响应）。
  3. **阅读器单章源覆盖交互（原生 DOM / `<dialog>` 层，严禁引入 Element Plus）**：
     - 在 `ReaderView.vue` 顶部操作栏与 `ChapterOverrideDialog.vue` 中使用原生 `<dialog>` + CSS Variables 实现单章换源覆盖：展示当前章覆盖状态（`is_overridden`），支持输入目标章节 `chapter_url` 与可选 `source_id` 提交 `setChapterOverride`（展示返回的 `content_preview` 预览并刷新当前章正文与 IndexedDB 缓存），支持一键清除当前章覆盖（`deleteChapterOverride`）。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `cd frontend && npm run generate:api && git -c safe.directory=D:/YLReader diff --exit-code -- src/api/schema.d.ts`
  - `cd frontend && npm run type-check && npm test`
  - `cd frontend && npx vite build --outDir $env:TEMP\ylreader-r3-dist --emptyOutDir`

---

## R4：前端离线 PWA 应用壳与移动端交互打磨（Offline PWA & Mobile UX Polish）

- **任务编号**：`R4`
- **涉及文件**：
  - `frontend/public/manifest.webmanifest`（新增 Web App Manifest）
  - `frontend/public/sw.js`（新增零外部依赖、安全同源静态壳缓存 Service Worker）
  - `frontend/index.html`
  - `frontend/src/pwa.ts`（新增受控 SW 注册与离线状态管理）
  - `frontend/src/pwa.test.ts`（新增）
  - `frontend/src/main.ts`
  - `frontend/src/style.css`
  - `frontend/src/reader-overlays.css`
  - `frontend/src/views/reader/ReaderView.vue`
  - `frontend/nginx.conf`
- **契约与安全要求**：
  1. **Service Worker 静态应用壳（支持离线冷启动阅读已缓存章节）**：
     - 仅缓存同源静态构建产物（`/`、`/index.html`、`/manifest.webmanifest` 及 `/assets/*`）；**严禁在 Service Worker Cache Storage 中缓存任何 `/api/*` 响应、JWT 令牌或用户隐私数据**（章节与设置离线数据仍完全由按 `owner` 隔离的 IndexedDB/Dexie 承担并随退出清理）。
     - `/index.html` 与 `/sw.js` 采用 Network-First（断网回退缓存壳），带哈希指纹的 `/assets/*` 采用 Cache-First；兼容 `frontend/nginx.conf` 现有严格 CSP（`script-src 'self'; worker-src 'self'` 或默认回退 `default-src 'self'`，不引入内联脚本或外部 CDN）。
  2. **移动端视口与阅读器触控体验打磨**：
     - `index.html` 与 `reader-overlays.css` 完善 `viewport-fit=cover`、`env(safe-area-inset-*)` 刘海/底部安全区留白、`100dvh` 动态视口与 `overscroll-behavior-y: contain`，杜绝 390px 移动视口下横向溢出或工具栏遮挡。
     - `ReaderView.vue` 支持阅读区点击/轻触唤醒或收起顶部/底部原生工具栏（不影响文本选择、链接或 `<dialog>` 内部点击），保持 60/120Hz 原生滚动与字符偏移恢复精度。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `cd frontend && npm run type-check && npm test`
  - `cd frontend && npx vite build --outDir $env:TEMP\ylreader-r4-dist --emptyOutDir`

---

## R5：生产运维体系与开源合规收口（Production Operations & Open-Source Compliance）

- **任务编号**：`R5`
- **涉及文件**：
  - `LICENSE`（新增开源许可证，默认 MIT 或用户确认的 SPDX 标准许可证）
  - `SECURITY.md`（新增安全漏洞报告策略与 SSRF/沙箱/密钥安全边界说明）
  - `backend/cmd/migrator/main.go`
  - `backend/cmd/migrator/main_test.go`
  - `backend/cmd/server/main.go`
  - `backend/cmd/server/security_config.go`
  - `backend/cmd/server/security_config_test.go`
  - `docker-compose.prod.yml`（新增带独立 `sandboxworker` 与安全加固的生产编排配方）
  - `.env.example`
  - `docs/deployment.md`
  - `README.md`
- **契约与安全要求**：
  1. **受控生产迁移与旧版数据导入 CLI（`backend/cmd/migrator`）**：
     - 保持默认 `migrator schema up` 对非测试库的拒绝保护不变（`scripts/check-db-target.py` 与 `Makefile` 的 `migrate` 目标不变）。
     - 新增显式生产升级确认模式：`migrator schema up --production-confirm-db=<dbname>`，要求 `DATABASE_URL` 显式包含 host/port/dbname 且与确认参数完全一致，在单事务 + `pg_advisory_xact_lock` 下执行版本账本检查与基线/增量升级，升级后立即执行 `repository.CheckSchema` 校验。
     - 新增 `migrator import-legado-sources --user=<username> --file=<path> [--dry-run]` 命令行入口，复用 `SourceService.Import` / `ImportExportRepository` 管线将旧版 Reader / Legado 书源 JSON 平滑导入指定账户并自动完成 Header 信封加密。
  2. **沙箱运维参数环境变量化与生产编排加固（`docker-compose.prod.yml`）**：
     - `backend/cmd/server` 支持可选环境变量 `YREADER_SANDBOX_INPUT_LIMIT_MB`（1..32，默认 32）、`YREADER_SANDBOX_INFLIGHT_MB`（默认 64，不低于 2×InputLimit）、`YREADER_SANDBOX_MAX_CONCURRENT`（1..32，默认 4），非法值在启动期 fail-closed 拒绝。
     - 新增 `docker-compose.prod.yml`：编排 `app`、`sandboxworker-1`、`sandboxworker-2`（基于 `backend/Dockerfile.sandboxworker`，`network_mode: "none"`、`read_only: true`、`cap_drop: ["ALL"]`、`security_opt: ["no-new-privileges:true"]`、`mem_limit: 256m`、`pids_limit: 64`、专用 `0700` IPC 卷挂载）、`postgres`、`redis` 与 `frontend`；所有数据库与 Redis 口令强制从 `.env` 注入，不暴露 Postgres/Redis 宿主端口。
  3. **开源合规与部署文档终稿**：
     - 补齐根目录 `LICENSE`、`SECURITY.md`，更新 `README.md` 与 `docs/deployment.md`（完整涵盖开发编排、生产 `docker-compose.prod.yml` 启用、HTTPS 反代 HSTS 建议、受控数据库升级与备份恢复演练指引）。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `python scripts/test-db-guard.py`
  - `cd backend && go test -mod=readonly -buildvcs=false -count=1 ./cmd/migrator ./cmd/server`
  - `docker compose -f docker-compose.prod.yml --env-file scripts/compose-check.env config --quiet`

---

## R6：全栈端到端回归、并发压测与 v1.0.0 发布流水线（Full-Stack E2E, Load Verification & Release Pipeline）

- **任务编号**：`R6`
- **涉及文件**：
  - `frontend/tests/r3-r4-parity.spec.ts`（新增覆盖单源创建/导出、备用源管理、单章覆盖、账户信息与 PWA 离线壳的 Playwright E2E）
  - `backend/internal/api/benchmark_concurrency_test.go`（新增 100 并发正文读取与换源模糊定位基准测试）
  - `scripts/verify-release-v1.py`（新增一键全栈发布门禁核验脚本）
  - `.github/workflows/release.yml`（新增 `v*` 标签触发的双架构镜像与 CLI 构建/Trivy 门禁工作流）
  - `docs/release-v1.0.0-verification.md`（新增 v1.0.0 最终发布验收与已知边界签发报告）
- **契约与安全要求**：
  1. **全栈 E2E 与并发性能验证**：
     - 在隔离 PostgreSQL/Redis 测试容器与测试后端上运行 Playwright E2E，验证 R1–R4 新增前后端链路及既有 11 项核心场景全部通过，`pageerror` 与 `unhandledrejection` 计数为 0。
     - 运行 100 并发章节正文读取（缓存命中与合成回源）及 `goleak` 协程泄漏检查，验证 p95 延迟与零协程泄漏。
  2. **多架构发布工作流（`.github/workflows/release.yml`）与最终签发报告**：
     - 校验 16 份冻结契约 SHA-256 摘要、`govulncheck`、前端 `npm audit`、全量单元测试、Linux `-race` 集成测试及 `backend`/`frontend`/`sandboxworker` × `linux/amd64`/`linux/arm64` 镜像构建与 Trivy 全严重度扫描。
     - 在 `docs/release-v1.0.0-verification.md` 中汇总全部实测指标、明确列出 v1.0.0 已交付范围与延后至 v1.1+ 的演进项（如 S3 对象存储驱动、大书异步导入任务、iPhone 实体设备手工复核记录），完成 v1.0.0 发布封板。
- **验证命令**：
  - `python scripts/verify-backup-contract.py`
  - `cd backend && go vet -mod=readonly -buildvcs=false -tags=integration ./...`
  - `cd backend && go test -mod=readonly -buildvcs=false -count=1 ./...`
  - `cd frontend && npm run type-check && npm test && npm run build`
  - `python scripts/verify-release-v1.py`
