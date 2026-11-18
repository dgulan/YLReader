# 受控恢复执行 CLI 入口与操作员交接闭环验收（H5）

## 本轮范围

依据先行设计 [backup-controlled-restore-cli-design.md](backup-controlled-restore-cli-design.md)，完成 `backup` CLI 受控执行入口 `restore` 与只读预览/诊断命令的严格隔离，复用 `restoreGuard` 全链门禁，并完成无网络隔离容器下的端到端进程级验证：

- **`backend/internal/backup/controlled_restore.go`**：
  - 定义 `ControlledRestoreParams`、`ControlledRestoreResult` 与 `RunControlledRestoreFromInput`：强制核验全部显式授权输入（64 位小写十六进制 `PlanDigest`、规范小写 `ConfirmTarget`、非零规范 `OwnerID`、安全相对键 `RequestKey`/`ConfigKey`/`PlanKey`/可选 `HandoverKey`、`safeAbsolute` 校验的 `HeaderKeysPath`/`PGRestorePath` 与 `1s..30m` 有界 `Timeout`）。
  - 执行阶段严格复用：`CheckInspectionIdentity` 与限制匹配 -> `VerifyPackage` 验证密文与身份 -> `acquireRestoreGuard`（目标库排他空库检查与双目录租约）-> `StageRecorded`（控制目录已有操作立即返回 `RESTORE_ALREADY_RECORDED` 禁止重放）-> `RunDatabaseWithHeaders`（独立事务导入并逐行验证 `book_sources.header` 密文与 AAD）-> `PublishObjects`（原子发布对象并落盘 `publication.json = "verified"`）。
  - 可选 `--handover-key`（`ylreader-handover-execution-spec` `partial_review`）：通过 `storage.ReadBackupSecret` 安全读取密钥并在 `defer clear()` 擦除，调用真实目标数据库/Redis/运行时与已发布对象探针采集并持久化 `handover-review.json`（8 pass / 2 unavailable），确认部分报告禁止进入 CAS 登记后输出 `RESTORE_HANDOVER_PARTIAL_UNREGISTERED`。
  - 定义 `SanitizeControlledRestoreResult`：凡遇 `ErrRestoreCommitUnknown`/`ErrOutcomeRecord`、`ErrPublication`、`errHandoverUnconfirmed`、`errRegistrationUnknown`、`context.Canceled`/`DeadlineExceeded`，零自动重试写入、零清理保留产物，固定设 `retry_safe = false` 且 `execution_authorized`/`ready_for_cutover`/`deployment_authorized` 恒为 `false`。
- **`backend/cmd/backup/main.go` & `backend/cmd/backup/restore.go`**：
  - 仅在 `restore` 子命令下路由至 `runRestore`，保留 `inspect`/`plan`/`status`/`behavior-status` 只读入口及各自独立参数白名单，拒绝互混执行专有参数（退出码 `2`）。
  - 在 `runRestore` 中对缺参、重参、非规范 UUID/SHA256、相对或含 `..` 路径、越界 `--timeout` 在打开任何目录前返回 `2`；目录不可用返回 `3`；关闭或标准输出失败返回 `5`；业务拒绝/部分审阅/不确定确认/取消返回 `4` 并输出脱敏 `ControlledRestoreResult`（支持 `json`/`text`）与静态 `status_code`。
- **测试与隔离验证脚本**：
  - 新增 [controlled_restore_test.go](file:///D:/YLReader/backend/internal/backup/controlled_restore_test.go) 与 [restore_test.go](file:///D:/YLReader/backend/cmd/backup/restore_test.go)：覆盖多参数边界校验、只读与执行命令隔离、超时上下文绑定、`ErrRestoreCommitUnknown`/`ErrPublication`/`errRegistrationUnknown`/`context.Canceled` 零重试零泄露与 `text`/`json` 退出码契约。
  - 新增 [restore_cli_linux_test.go](file:///D:/YLReader/backend/cmd/backup/restore_cli_linux_test.go)（`//go:build integration && linux`）与 [verify-backup-restore-cli.py](file:///D:/YLReader/scripts/verify-backup-restore-cli.py)：构建真实 `backup` 与 `./cmd/backup` 集成测试二进制，在无外网 `postgres:16` + 双 `redis:7-alpine` 容器内验证 7 项独立进程场景。

本轮未注册任何 Web Server 路由，未修改 `docs/openapi.yaml`（43 个公开操作）、`docs/schema.sql`、备份归档格式或 16 份冻结契约摘要。

## 验证结果

1. **契约与静态检查**：
   - `python scripts/verify-backup-contract.py`：通过（65 项契约检查全部通过，16 份冻结契约摘要匹配，报告 `.toolchain/backup-contract-checks.json`）。
   - `go vet -mod=readonly -tags=integration ./...`、`gofmt -s -l internal/backup cmd/backup`、`python -m py_compile scripts/verify-backup-restore-cli.py` 与 `git -c safe.directory=D:/YLReader diff --check`：全部通过；未运行 `-race`。
2. **Windows 本机单元测试**：
   - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup`：全部通过（见本轮最终命令实测输出）。
3. **独立预览 CLI 容器回归**：
   - `python scripts/verify-backup-preview-cli.py`：通过（报告 `.toolchain/backup-preview-cli-48f8db3cdf05/result.json`，`passed: true`、`passing_events: 10`、`cleanup` 双容器均为 `true`、`execution_authorized: false`、`deployment_authorized: false`）。
4. **独立受控恢复 CLI 容器验证**：
   - `python scripts/verify-backup-restore-cli.py`：通过（报告 `.toolchain/backup-restore-cli-16b2b48149dc/result.json`，`passed: true`、`passing_events: 8`、4 个自有临时容器 `cleanup` 均为 `true`）。
   - 7 项子测试全部 `PASS`（4.22s，无跳过/失败）：`reject_preview_flag_escalation`、`reject_missing_authorization_flags`、`reject_mismatched_plan_digest`、`reject_occupied_target`、`reject_wrong_header_keyring_rollback`、`controlled_restore_core_published_and_no_replay`（`RESTORE_VALIDATING_PUBLISHED` -> 重放拒绝 -> `backup status` 确认 `records_only` + `validating`）、`controlled_restore_partial_handover_no_registration`（`RESTORE_HANDOVER_PARTIAL_UNREGISTERED` -> `backup status` 确认 `blocked` / `report_claims = partial` / `registration = absent`）。
5. **Linux 隔离 PostgreSQL 16.15 + Redis + 真实归档/TLS 全量联合验收**：
   - 复用命令：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-restore-cli-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

   - 结果：通过（报告 `.toolchain/backup-restore-db-cb4dc1796076/result.json`，`passed: true`、`passing_events: 129`、0 失败/跳过，4 个自有临时容器 `cleanup` 均为 `true`）。
   - 实测：`bound_behavior_review` 为 `profile = "bound-https-account-verification-v1"`、`status = "complete"`、`verified_checks = 12`、`unavailable_checks = 0`；`bound_registration` 为 `scope = "bound-behavior-review-registration"`、`state = "registered"`、`base_generation = 4`、`next_generation = 5`、`operation_phase = "validating"`；`ready_for_cutover = false`、`deployment_authorized = false`。

| 证据项 | SHA-256 / 值 |
|---|---|
| Linux 受控恢复测试二进制（`.toolchain/backup-restore-cmd-linux.test`） | `3ef0e3b5163d9c00e4d8ea27917fa6d56712c891d79c314a99c85e7c77b7218f` |
| Linux CLI 二进制（`.toolchain/backup-restore-linux` / `backup-preview-linux`） | `27754b8f33a8798c1b2eae070ca96d113849d4f4b14fa8b9a943eccda00e4f14` |
| Linux 内核全链路测试二进制（`.toolchain/backup-restore-cli-linux.test` / `backup-preview-cli-linux.test`） | `88f2842fecf60f80b3bf23a9b2c3d5ea70bcf50d9d93fb2febbbaaaf54c64fcf` |
| 合成数据库归档 | `3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303` |
| PostgreSQL 16.15 镜像 | `sha256:f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657` |
| Redis 镜像 | `sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b` |

## H1–H5 完成后的剩余边界说明

- **不自动切换生产流量与不推断持久确认**：即便完成 `backup restore`（`phase = validating`）与完整 12 项 `bound-behavior-review` 的 CAS 登记，所有输出（`ControlledRestoreResult`、`RestoreStatus`、`BehaviorLedgerDiagnostic`、`handoverRegistration`）均恒定保持 `execution_authorized = false`、`ready_for_cutover = false`、`deployment_authorized = false` 与 `persistence_confirmation = "not_inferred"`；最终生产切流与常驻进程启停仍需运维/操作员按人工交接检查单独立执行。
- **行为审阅同意链边界**：单次 `backup restore`（含 `--handover-key` `partial_review`）止于对象发布及部分交接审阅采集；完整 12 项 HTTPS 行为闭环与 CAS 登记（H1–H4）须在目标对象发布及新实例运行期就绪后、基于绑定 `[OperationSHA, AttemptSHA, PublicationSHA]` 与 `RuntimeSHA` 的实时 `behaviorConsent` 独立执行（已在全量隔离联合测试中端到端验证通过）。
- **目录与平台能力边界**：`storage.OpenBackupFiles` 依赖 Linux 安全目录描述符校验（Windows 仅支持构建、语法、静态检查与非内核态单元测试，完整进程与数据库/Redis/HTTPS 隔离验收均在受限 Linux 容器中执行）；本轮未接触 `references\` 或任何生产数据库，未执行 git commit/reset/checkout/stash。
