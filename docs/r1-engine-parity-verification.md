# R1 验收记录：后端契约对齐与书源引擎能力补齐

基线分支：`codex/pre-release-20261113`（`HEAD = 74c23ccb`），遵循 `docs/release-v1-execution-plan.md` 任务 **R1**。未触碰 `backend/internal/backup`、`backend/cmd/backup` 或任何冻结契约文件。

## 变更摘要

1. **`setChapterOverride` 试抓正文与缓存刷新对齐**
   - `ContentService.TrialFetchForOverride`（[content.go](file:///d:/YLReader/backend/internal/service/content.go)）在落库前完成 10s 预算试抓、分页完整性校验与替换净化，并返回提交后写入缓存回调。
   - `OverrideService.SetWithPreview`（[overrides.go](file:///d:/YLReader/backend/internal/service/overrides.go)）先校验与试抓（失败不落库），事务提交后刷新 `yreader:cache:content:<book_id>:<source_id>:<chapter_index>`，并按 Unicode code point 截取前 512 字符填充 `ChapterOverride.content_preview`（[handlers_overrides.go](file:///d:/YLReader/backend/internal/api/handlers_overrides.go)）。
   - [main.go](file:///d:/YLReader/backend/cmd/server/main.go) 与 [api_test.go](file:///d:/YLReader/backend/internal/api/api_test.go) 已注入 `WithContent(...)`。
2. **书源静态 `src.Header` 安全出网贯通**
   - 新增 [source_header.go](file:///d:/YLReader/backend/internal/service/source_header.go)（`parseSourceHeaders` / `mergeHeaders`）：仅放行 `User-Agent`、`Accept`、`Accept-Language`、`Referer`、`Origin`、`Cookie`、`Authorization` 与 `X-*`，拒绝含 `\r`/`\n`/`\x00`/非法键名的请求头，阻断 `Host`/`Content-Length`/`Transfer-Encoding`/`Proxy-*` 等危险头。
   - 已接入 [content.go](file:///d:/YLReader/backend/internal/service/content.go)、[toc.go](file:///d:/YLReader/backend/internal/service/toc.go)、[debug.go](file:///d:/YLReader/backend/internal/service/debug.go) 与 [search.go](file:///d:/YLReader/backend/internal/service/search.go)（`searchUrl` 显式头优先覆盖 `src.Header`）。
3. **JS 沙箱 `aesEncode` / `aesDecode` 扩展模式**
   - [bridge.go](file:///d:/YLReader/backend/internal/engine/sandbox/bridge.go) 保留两参数（AES-128-ECB + MD5 key + PKCS7）完全向后兼容，新增三/四参数 `(data, key, transformation, iv)` 支持 `AES/CBC/PKCS5Padding`、`AES/CBC/PKCS7Padding`、`AES/ECB/PKCS5Padding`、`AES/ECB/PKCS7Padding` 及 `NoPadding`（16/24/32 字节原始 key 或 MD5 回退），并补充 `java.aesBase64DecodeToString` 与 `java.aesDecodeToString` 别名；异常参数或填充错误一律安全返回空串。

## 验证结果

- `python scripts/verify-backup-contract.py`：**通过**（65 contract checks passed; 16 frozen hashes match）。
- `gofmt -l ./cmd/server ./internal/api ./internal/engine/sandbox ./internal/service`：**无未格式化文件**。
- `go vet -mod=readonly -buildvcs=false -tags=integration ./...`：**通过（exit 0）**（含新增集成测试 [overrides_preview_test.go](file:///d:/YLReader/backend/internal/api/overrides_preview_test.go) 与更新后的 [bookshelf_test.go](file:///d:/YLReader/backend/internal/api/bookshelf_test.go)、[toc_test.go](file:///d:/YLReader/backend/internal/api/toc_test.go)）。
- `go test -mod=readonly -buildvcs=false -count=1 ./internal/service ./internal/api`：**通过（exit 0）**（`internal/service` 1.464s、`internal/api` 3.591s；含 [source_header_outbound_test.go](file:///d:/YLReader/backend/internal/service/source_header_outbound_test.go) 单元测试，3 项需 `TEST_DATABASE_URL` 的服务测试按条件跳过）。
- `go test -mod=readonly -buildvcs=false -count=1 ./internal/engine/sandbox ./internal/engine/fetcher`（放行回环套接字）：**通过（exit 0）**（`sandbox` 7.092s、`fetcher` 1.541s；含 `TestSandbox_AESModes`）。
- 边界说明：本机未配置 `TEST_DATABASE_URL`，`go test -tags=integration` 未连接真实 PostgreSQL 执行（将在 CI 或隔离测试库阶段运行）。
