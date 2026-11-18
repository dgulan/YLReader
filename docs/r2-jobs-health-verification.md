# R2 验收记录：后台任务扩展与书源健康度指标

基线分支：`codex/pre-release-20261113`（R1 锁定提交 `ffc38586`），遵循 `docs/release-v1-execution-plan.md` 任务 **R2**。未触碰 `backend/internal/backup`、`backend/cmd/backup` 或任何冻结契约文件。

## 变更摘要

1. **书源健康度指标与 `source_failures` 失败归因**
   - 新增 [source_health.go (repository)](file:///d:/YLReader/backend/internal/repository/source_health.go)：实现 `RecordSuccess`（清零 `failure_count`、指数滑动平均更新 `respond_time`、更新 `last_checked_at`/`last_success_at`、关闭活跃 `source_failures`）与 `RecordFailure`（递增 `failure_count`、更新 `last_checked_at`、写入 `source_failures` 记录且 `expires_at > occurred_at`）。
   - 新增 [source_health.go (service)](file:///d:/YLReader/backend/internal/service/source_health.go)：实现 `ClassifySourceFailure`（将超时、SSRF/Challenge 拦截、规则错误、不支持规则、401/403 鉴权与网络故障映射为安全脱敏的 `failure_kind` 与短文案，严禁透出 `err.Error()` 原文/URL/凭据）及忽略 `context.Canceled` 的 500ms 有界上报。
   - 已接入 [search.go](file:///d:/YLReader/backend/internal/service/search.go)、[toc.go](file:///d:/YLReader/backend/internal/service/toc.go)（成功写入严格位于 `LockTOCSource` 事务提交之后，避免触发 `trg_sources_version` 误冲突）与 [content.go](file:///d:/YLReader/backend/internal/service/content.go)。
2. **后台任务多类型执行器与可观测指标**
   - [jobs.go](file:///d:/YLReader/backend/internal/service/jobs.go)：在保留 `full_cache` 原有行为与 Advisory Lock 语义基础上，补齐 `toc_refresh`、`chapter_prefetch`（最多 20 章有界预热）与 `source_health` 执行器；不支持类型以独立 `context.Background()` 落 `failed` 终态。
   - [main.go](file:///d:/YLReader/backend/cmd/server/main.go)：注入 `WithTOC(tocSvc)` 与 `WithJobMetric`，将 `queued`/`running`/`succeeded`/`failed`/`canceled` 事件打点至 `yreader_jobs_total`。

## 验证结果

- `python scripts/verify-backup-contract.py`：**通过**（65 contract checks passed; 16 frozen hashes match）。
- `gofmt -l ./cmd/server ./internal/repository ./internal/service ./internal/api`：**无未格式化文件**。
- `go vet -mod=readonly -buildvcs=false -tags=integration ./...`：**通过（exit 0）**（含新增 [source_health_test.go (repository)](file:///d:/YLReader/backend/internal/repository/source_health_test.go)）。
- `go test -mod=readonly -buildvcs=false -count=1 ./internal/repository ./internal/service ./internal/api`：**通过（exit 0）**（含 [source_health_test.go (service)](file:///d:/YLReader/backend/internal/service/source_health_test.go) 脱敏分类、取消忽略与指标回调单测）。
- `go build -mod=readonly -buildvcs=false -o $env:TEMP\ylreader-r2-server.exe ./cmd/server`：**通过（exit 0）**。
- 边界说明：本机未配置 `TEST_DATABASE_URL`，`go test -tags=integration` 需在隔离 PostgreSQL 测试库或 CI 中实际运行验证 `source_failures` 与 `book_sources` 触发器联动。
