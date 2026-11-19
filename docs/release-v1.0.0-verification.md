# YLReader v1.0.0 最终发布验收与已知边界签发报告

基线分支：`codex/pre-release-20261113`（R1–R5 已提交至 `6e754c23`，本报告完成任务 **R6** 全部验收项）。
严格遵守 [AGENTS.md](file:///d:/YLReader/AGENTS.md) 净室隔离与契约先行原则，43 个公开 OpenAPI `operationId`、[schema.sql](file:///d:/YLReader/docs/schema.sql)、16 份冻结契约 SHA-256 摘要以及 `backend/internal/backup` / `backend/cmd/backup` 均保持完整无变更。

---

## 1. v1.0.0 交付范围汇总（R1–R6）

| 工作包 | 核心交付内容 | 对应验证依据 |
|---|---|---|
| **R1** | `setChapterOverride` 真实 SSRF 预检与试抓预览（512 字符）及缓存刷新；书源静态 `src.Header` 白名单清洗与出网贯通；`goja` 沙箱 `aesEncode`/`aesDecode` 3/4 参数 CBC/ECB 模式扩展 | [r1-engine-parity-verification.md](file:///d:/YLReader/docs/r1-engine-parity-verification.md) |
| **R2** | `book_sources` 健康度指标与 `source_failures` 脱敏归因闭环；`JobService` 补齐 `toc_refresh` / `chapter_prefetch` / `source_health` 执行器与 `yreader_jobs_total` 指标 | [r2-jobs-health-verification.md](file:///d:/YLReader/docs/r2-jobs-health-verification.md) |
| **R3** | 前端 `reader.ts` 补齐全部 43 个冻结 API；单源创建/导出、409 草稿保留与版本同步、在线书备用源映射管理、`/auth/me` 账户信息卡片、原生 `<dialog>` 单章换源覆盖（无 Element Plus） | [r3-r4-parity.spec.ts](file:///d:/YLReader/frontend/tests/r3-r4-parity.spec.ts) |
| **R4** | 零外部依赖同源静态壳 Service Worker（[sw.js](file:///d:/YLReader/frontend/public/sw.js)，严禁缓存 `/api/*`）、Web App Manifest（[manifest.webmanifest](file:///d:/YLReader/frontend/public/manifest.webmanifest)）、移动端安全区与 `100dvh` 交互打磨；R6 联调修复离线预缓存资产在 `Vary: Origin` 下的 `{ ignoreVary: true }` 匹配问题 | [pwa.ts](file:///d:/YLReader/frontend/src/pwa.ts) / [sw.js](file:///d:/YLReader/frontend/public/sw.js) |
| **R5** | 根目录 [LICENSE](file:///d:/YLReader/LICENSE) 与 [SECURITY.md](file:///d:/YLReader/SECURITY.md)；`cmd/migrator` 受控生产升级确认与 `import-legado-sources` 导入 CLI；沙箱资源环境变量化与加固生产编排 [docker-compose.prod.yml](file:///d:/YLReader/docker-compose.prod.yml)；部署运维指南 [deployment.md](file:///d:/YLReader/docs/deployment.md) | [deployment.md](file:///d:/YLReader/docs/deployment.md) |
| **R6** | Playwright R3/R4 端到端套件 [r3-r4-parity.spec.ts](file:///d:/YLReader/frontend/tests/r3-r4-parity.spec.ts)；100 并发读取/换源模糊定位基准与 `goleak` 校验 [benchmark_concurrency_test.go](file:///d:/YLReader/backend/internal/api/benchmark_concurrency_test.go)；一键门禁脚本 [verify-release-v1.py](file:///d:/YLReader/scripts/verify-release-v1.py)；多架构发布流水线 [release.yml](file:///d:/YLReader/.github/workflows/release.yml) | 本报告 §2 实测结果 |

---

## 2. 本次 R6 实测门禁与指标汇总

执行环境：Windows 11 amd64（13th Gen Intel Core i5-13500），Go `go1.24.2`（`go.mod` toolchain `go1.26.11`），Node `v26.13.0`，Python `3.15.2`，临时隔离 `postgres:16` 与 `redis:7-alpine` 容器（`--tmpfs`，测试后自动销毁）。

### 2.1 一键全栈门禁执行结果

1. **`python scripts/verify-release-v1.py`（默认本地门禁）**：**通过（exit 0）**
2. **`python scripts/verify-release-v1.py --with-e2e`（含临时容器集成测试、并发压测与 Playwright E2E）**：**通过（exit 0）**

各门禁子项实测如下：

| 门禁项 | 命令 / 目标 | 实测结果 |
|---|---|---|
| 冻结契约与备份协议 | `python scripts/verify-backup-contract.py` | **通过**：65 contract checks passed；16 份冻结文件 SHA-256 全部匹配 |
| OpenAPI 操作数门禁 | `docs/openapi.yaml` `operationId` 计数 | **通过**：严格 43 个唯一 `operationId` |
| 数据库目标安全门禁 | `python scripts/test-db-guard.py` | **通过**：61/61 单元测试通过 |
| Compose 静态编排校验 | `docker compose -f docker-compose.yml / docker-compose.prod.yml --env-file scripts/compose-check.env config --quiet` | **通过** |
| Go 格式与静态检查 | `gofmt -l .` + `go vet -mod=readonly -buildvcs=false -tags=integration ./...` | **通过**（0 未格式化文件，exit 0） |
| 后端全量单元测试 | `go test -mod=readonly -buildvcs=false -p 1 -timeout 180s -count=1 ./...` | **通过**：全部 18 个测试包通过（含内存模式 `TestConcurrencyReadAndRelocate_NoGoroutineLeak` 与 `goleak.VerifyNone`） |
| 后端可执行产物编译 | `go build ./cmd/server` / `./cmd/migrator` / `./cmd/sandboxworker` | **通过** |
| 后端全量集成测试 | `go test -mod=readonly -buildvcs=false -tags=integration -p 1 -parallel 1 -timeout 300s -count=1 ./...`（隔离 PG16 + Redis7） | **通过**：全部测试包通过（`internal/api` 78.243s、`internal/repository` 48.975s、`internal/service` 10.887s、`internal/backup` 1.703s、`internal/engine/sandbox` 6.294s 等） |
| 前端类型契约同步 | `npm run generate:api` + `git diff --exit-code -- frontend/src/api/schema.d.ts` | **通过**（0 差异） |
| 前端类型检查与单测 | `npm run type-check` + `npm test` | **通过**：`vue-tsc` 0 错误；Vitest 24 个文件、106 项测试全部通过（11.25s） |
| 前端生产构建与 PWA 校验 | `vite build` + `manifest.webmanifest` / `sw.js` 检查 | **通过**：1694 个模块构建完成，`sw.js` 已注入版本与静态资产列表且排除 `/api/` |
| 全栈 Playwright E2E | `E2E_READING_PREVIEW=1 npm run test:e2e`（生产预览 + 真实 Go 后端 + PG16 + Redis7） | **通过（38 passed, 4 skipped, 0 failed, 2.0m）** |

> 说明：Playwright E2E 中 4 个跳过项分别为依赖独立 `E2E_PRODUCTION=1` 开关的 `modern-web.spec.ts:117`、依赖长时压测开关 `E2E_SOAK_SECONDS` 的 `reader-soak.spec.ts` 以及依赖外部样本文档 `E2E_STAGE3_TXT` 的 `stage3-reading.spec.ts` 2 条用例；包含 `r3-r4-parity.spec.ts` 在内的全部 38 项核心与 R3/R4 端到端用例均实测通过，且 `r3-r4-parity.spec.ts` 中 `pageerror` 与 `unhandledrejection` 均为 0。

### 2.2 100 并发压测与 `goleak` 零泄漏实测指标

1. **内存/合成回源模式（无 `TEST_DATABASE_URL`，直接驱动 HTTP Handler + `cache.Memory` + `parser` + `sandbox`）**：
   - `TestConcurrencyReadAndRelocate_NoGoroutineLeak`（100 并发）：**通过（0.15s）**
     - 合成抓取 + 解析：`p95 = 693.5µs`
     - 缓存命中 + `/readyz` HTTP：`p95 = 76.387ms`
     - 模糊章节重定位（200 候选）：`p95 = 53.0812ms`
     - `goleak.VerifyNone`：**0 协程泄漏**
   - `BenchmarkConcurrencyContentRead100-4`（5x）：`1313920 ns/op`，`0.7410 p95_ms`
2. **真实 PostgreSQL 16 + `*pgxpool.Pool`（`MaxConns >= 32`，每协程独立事务，显式关闭 HTTP Server 与 `pool.Close()` 后执行 `goleak.VerifyNone`）**：
   - `TestConcurrencyReadAndRelocate_NoGoroutineLeak`（100 并发）：**通过（10.18s）**
     - 100 并发冷启动回源读章并回填缓存：`p95 = 3.8682916s`（`upstream_hits = 92`）
     - 100 并发缓存命中读章：`p95 = 749.2003ms`（0 新增回源请求）
     - 100 并发 `POST /books/{bookId}/switch-source`（`dry_run=true`，触发 `pg_trgm` `similarity >= 0.6` 模糊定位）：`p95 = 4.5939534s`
     - `goleak.VerifyNone`：**0 协程泄漏**
   - `BenchmarkConcurrencyContentRead100-4`（3x）：`823321633 ns/op`，`1124 p95_ms`

---

## 3. CI/CD 发布流水线与已知边界

1. **多架构发布工作流（[.github/workflows/release.yml](file:///d:/YLReader/.github/workflows/release.yml)）**：
   - 由 `v*` 标签或 `workflow_dispatch` 触发，串联 `contracts-and-security`（16 份冻结摘要、43 `operationId`、DB 门禁、Compose 校验、`govulncheck@v1.9.0` 双架构扫描、`npm audit`）、`backend-race-integration`（Linux `-race -tags integration` + 100 并发与 `goleak`）、`frontend-and-e2e`、`cli-binaries`（`linux/amd64` 与 `linux/arm64` 静态二进制打包与 SHA-256）、`images`（`backend`/`frontend`/`worker` × `amd64`/`arm64` 构建与 Trivy `UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL` 零忽略扫描）及 `release-bundle`（生成 `SHA256SUMS` / `IMAGES_SHA256SUMS` 并于标签发布时创建 GitHub Release）。
   - **未在本机直接执行的 CI 项声明**：本机为 Windows 环境且未安装 `govulncheck` / Trivy 本地 CLI，因此 Linux `-race` 集成、跨平台 `govulncheck` 扫描及双架构容器镜像构建与 Trivy 扫描将在推送 `v*` 标签或于 GitHub Actions 触发 `.github/workflows/release.yml` 时实际运行并留存工件。

2. **延后至 v1.1+ 的演进项（已知边界）**：
   - **S3 / MinIO 对象存储驱动**：v1.0.0 仅交付基于统一 `StorageProvider` 接口的 `LocalFS` 驱动（适配单机与 Docker Volume），S3 兼容驱动延后至 v1.1+。
   - **超大书籍异步后台导入任务**：v1.0.0 本地 TXT/EPUB 导入采用单请求有界超时（默认 `90s`，上限 `100 MiB`），异步化大书导入队列延后至 v1.1+。
   - **iPhone / iOS Safari 实体设备手工复核**：移动端适配已通过 Playwright Chromium 320px/390px/812px 视口与安全区样式验证，iPhone 实体设备的真机 PWA 安装与横竖屏手势复核延后至真机验收轮次记录。
   - **历史 `chapter_cache` 表物理清理（A2）**：未获独立变更授权，v1.0.0 保持 [schema.sql](file:///d:/YLReader/docs/schema.sql) 冻结现状不执行物理删表。
