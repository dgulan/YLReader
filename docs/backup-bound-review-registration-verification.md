# 通用行为审阅报告的状态诊断展示与 CAS 登记衔接验收（H4）

## 本轮范围

依据先行设计 [backup-bound-review-registration-design.md](backup-bound-review-registration-design.md)，完成通用绑定行为审阅报告（`operations/<UUID>/behavior-account-review.json`）在 `backup status` / `backup behavior-status` 中的只读状态诊断展示，并将完整 12 项通用报告接入操作版本 CAS 登记门禁（`handover-registration.json` + `state.json` `generation` 递增，保持 `phase == "validating"`），同时保持 `execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 恒为 `false`：

- **`backend/internal/backup/behavior_review.go`**：
  - 在 `confirmedBehaviorRun` 中新增私有 `reviewPersisted bool`，仅在 `persistBoundBehaviorReview` 确认落盘及前后账本绑定一致后置 `true`；若持久化确认丢失，即使磁盘上存在报告文件也拒绝进入 CAS 登记。
  - 新增 `boundBehaviorReview.validateBindingsFor`、`validateCompleteFor` 与 `boundBehaviorReviewStore.Load`；`validateCompleteFor` 强制要求 `Status == "complete"`、全部 12 项均为 `"pass"`、且与三控制记录及实时目标三身份严格一致（`partial` 或任一 `unavailable`/`fail` 一律返回 `errBehaviorReview`）。
- **`backend/internal/backup/handover_registration.go`**：
  - `handoverRegistration.validate()` 扩展允许 `Scope == "bound-behavior-review-registration"`（与既有 `"isolated-review-registration"` 严格区分 `ReportSHA` 归属）。
  - 抽取共享 CAS 登记核心 `registerReviewCAS`（`prepared` 创建 -> `state.json` `CompareAndSwap` 递增 `generation` 并保持 `phase == "validating"` -> 双重确认后 `CompareAndSwap` 更新为 `registered`；确认丢失或冲突返回 `errRegistrationUnknown` / `storage.ErrConflict`，保留既有文件禁止重放）。
  - 新增 `registerBoundBehaviorReview` 与 `(g *restoreGuard) registerBoundReview`：登记前重新执行 `inputs.probes(p)`、`probeHandoverDataSessions` 与 `recheckBindingsLocked`，并锁存 `g.registrationStarted`。
- **`backend/internal/backup/status_report.go` & `backend/internal/backup/status.go`**：
  - 定义 `BoundReviewDiagnostic`、`defaultBoundReviewDiagnostic`、`readOptionalBoundReview`、`evaluateBoundReview` 与 `applyBoundStatusToRestore`；`persistence_confirmation` 恒为 `"not_inferred"`，`execution_authorized` / `ready_for_cutover` / `deployment_authorized` 恒为 `false`。
  - 在 `RestoreStatus` 中嵌入 `BoundReview`（JSON `bound_behavior_review`），在 `readStatusSnapshot` 中保持既有的 6 次双快照读取结构并纳入 `behavior-account-review.json` 校验，正确区分 `absent`、`unavailable`、`changed`、`invalid`、`inconsistent`、`drifted`、`expired`、`partial`、`complete_unregistered`、`registration_pending`、`complete_registered`，并据此更新 `observed_registered` / `registration_pending` / `review_only` / `blocked` / `inconsistent` 判定。
- **`backend/internal/backup/behavior_status_report.go` & `backend/internal/backup/behavior_ledger.go`**：
  - 在 `BehaviorLedgerDiagnostic` 中嵌入 `BoundReview`（JSON `bound_behavior_review`），保留 `stableBehaviorLedger` 不变，在其后通过 `readBoundBehaviorStatusSnapshot` 与 `attachBoundReviewToBehaviorDiagnostic` 对控制记录、通用报告与登记链进行双读防漂移校验，核对 `ConsentID`、`behavior_ledger_sha256`、`AccountCount` 及 `Identity.Runtime`。
  - 当 `handover-registration.json` 以 `bound-behavior-review-registration` 将 `state.json` 从 `reg.BaseGeneration` 推进至 `reg.NextGeneration` 后，账本主状态因意向绑定于旧世代而如实保留 `stale`（`stale_records_outcome_not_inferred`），同时依据不可篡改的 `BaseSHA`/`NextSHA`/`intent`/`actions` 摘要验证绑定，将 `bound_behavior_review.status` 准确判定为 `complete_registered`（窗口过期时仍精确判定为 `expired`）。
- **`backend/cmd/backup/main.go` & `backend/cmd/backup/behavior_status.go`**：
  - 新增共用文本渲染 `formatBoundReviewText`，在 `backup status` 与 `backup behavior-status` 的 text/JSON 输出中无歧义展示通用报告存在性、状态、待验项清单（`pending_checks`）、时效、控制绑定与版本及登记绑定；`renderBehaviorStatus` 在 `bound_behavior_review.status` 属于 `partial`/`expired`/`drifted`/`inconsistent`/`invalid`/`unavailable`/`changed`/`registration_pending` 时返回退出码 `4`。
- **测试与验证脚本配套更新**：
  - `backend/internal/backup/handover_registration_test.go`、`status_test.go`、`behavior_ledger_test.go`、`backend/cmd/backup/main_test.go`、`behavior_status_test.go`：补充通用报告 CAS 登记、部分报告（`10/2`、`11/1`）/过期/篡改/持久化确认丢失拒绝、确认丢失保留与不可重放、`DiagnoseBehaviorLedger` 登记后 `stale` + `complete_registered` / `expired` 以及双 CLI 的 JSON/text 与退出码单测。
  - `backend/internal/backup/status_cli_linux_test.go`：新增独立无网络容器进程测试 `TestBackupBoundReviewStatusCLIIsolated`（验证真实 `backup status --format json/text` 对已登记通用报告的输出与退出码 `0`，同时保持 `TestBackupStatusCLIIsolated` 7 个子测试结构不变）。
  - `backend/internal/backup/behavior_review_integration_test.go` 与 `scripts/verify-backup-restore-db.py`：在 `BACKUP_HANDOVER_REGISTER=1` 时验证完整 `bound-behavior-review` 执行 CAS 登记（`generation 4 -> 5`、`phase == "validating"`）、禁止重放，以及 `DiagnoseRestoreStatus`（`observed_registered` + `complete_registered`）与 `DiagnoseBehaviorLedger`（`stale` + `complete_registered`）。

本轮未修改 43 个公开 API、`docs/schema.sql`、备份格式、16 份冻结契约摘要或旧 `isolated-review-registration` 语义。

## 验证结果

1. **契约与静态检查**：
   - `python scripts/verify-backup-contract.py`：通过（65 项契约检查全部通过，16 份冻结契约摘要匹配，报告 `.toolchain/backup-contract-checks.json`）。
   - `go vet -mod=readonly -tags=integration ./...`、`gofmt -s -l internal/backup cmd/backup`、`python -m py_compile scripts/verify-backup-restore-db.py` 与 `git -c safe.directory=D:/YLReader diff --check`：全部通过（无输出/无告警）；未运行 `-race`。
2. **Windows 本机单元测试**：
   - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup`：全部通过（`internal/backup` 3.144s，`cmd/backup` 0.152s）。
3. **独立无网络 Linux 容器 CLI 验证**：
   - `python scripts/verify-backup-status-cli.py`：通过（报告 `.toolchain/backup-status-cli-92f31f193b07/result.json`，`passed: true`、`cleanup: true`、`passing_events: 8`）。
   - `python scripts/verify-backup-behavior-status-cli.py`：通过（报告 `.toolchain/backup-behavior-status-cli-a2ceff4e7d83/result.json`，`passed: true`、`cleanup: true`、`passing_events: 17`、`behavior_checks: 8`、`prior_status_checks: 7`）。
   - 独立无网络容器运行 `^TestBackupBoundReviewStatusCLIIsolated$`（证据 `.toolchain/backup-bound-status-cli-ff0e176c7c8e/tests.txt`）：通过（`status_json_bound_registered` 与 `status_text_bound_registered` 共 2 项 subtest 全部 `PASS`，容器销毁 `RM 0`）。
4. **Linux 隔离 PostgreSQL 16.15 + Redis + 真实归档/TLS 联合验收**：
   - 复用命令：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-bound-registration-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

   - 结果：通过（报告 `.toolchain/backup-restore-db-2255f43b1114/result.json`，`passed: true`、`passing_events: 129`、0 失败/跳过，4 个自有临时容器 `cleanup` 均为 `true`）。
   - `bound_registration` 实测：`scope = "bound-behavior-review-registration"`、`state = "registered"`、`base_generation = 4`、`next_generation = 5`、`operation_phase = "validating"`；`bound-review-registration.json` 中 `ready_for_cutover = false`、`deployment_authorized = false`；`bound_behavior_review` 实测 `profile = "bound-https-account-verification-v1"`、`status = "complete"`、`verified_checks = 12`、`unavailable_checks = 0`；重放登记被拒绝且不推进 `generation`。

| 证据项 | SHA-256 / 值 |
|---|---|
| Linux 测试二进制（`.toolchain/backup-bound-registration-linux.test` / `backup-status-cli-linux.test` / `backup-behavior-status-cli-linux.test`） | `826e9d5baada3508820d98ea9152ada1bff81e4cc5ecd316f94be7c9eff221d7` |
| Linux CLI 二进制（`.toolchain/backup-status-linux` / `backup-behavior-status-linux`） | `7c958303a25ecc94a2209f1cf88a86fd3ea5160443e8d8fc613d865a33713b9f` |
| 合成数据库归档 | `3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303` |
| PostgreSQL 16.15 镜像 | `sha256:f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657` |
| Redis 镜像 | `sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b` |

## 进入 H5 前的边界说明

- 完整 12 项 `bound-behavior-review` 已实现 `status`/`behavior-status` 只读状态展示与操作版本 CAS 登记衔接（`generation` 递增并保持 `phase == "validating"`），但 `execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 仍恒为 `false`，且磁盘诊断不会推断未确认的持久化完成。
- 受控恢复执行 CLI 子命令、多参数授权凭据检查及操作员交接闭环留待 **H5** 实现；H4 不包含任何自动切流或生产进程启动逻辑。
- 本轮未接触 `references\` 或任何生产数据库，未执行 git commit/reset/checkout/stash。
