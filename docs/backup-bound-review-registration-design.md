# 通用行为审阅报告的状态诊断展示与 CAS 登记衔接设计（H4）

状态：H4 内部先行设计，不修改 43 个公开 API、`docs/schema.sql`、备份归档格式、16 份冻结契约摘要；即使完整 12 项 `bound-behavior-review` 完成操作版本 CAS 登记，`state.json` 的 `phase` 仍保持 `validating`，且 `execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 仍恒为 `false`。

## 1. 目标与边界

1. 扩展 `backup status` 与 `backup behavior-status` 只读诊断输出，无歧义展示 `operations/<UUID>/behavior-account-review.json`（即通用绑定行为审阅报告）的存在性、完整/部分状态、待验项清单（`pending_checks`）、过期/漂移冲突、与控制记录版本及动作账本的绑定关系，且始终保持 `persistence_confirmation = "not_inferred"`。
2. 将通用完整报告（`status == "complete"` 且全部 12 项为 `"pass"`）接入操作版本 CAS 登记（`handover-registration.json` + `state.json` `generation` 递增，保持 `phase == "validating"`）：部分报告（`status == "partial"` 或含 `unavailable`/`fail`）、过期报告、绑定漂移报告或持久化确认丢失报告严禁进入 CAS 登记；确认丢失或冲突时保持只读保留且不可重放。

## 2. 通用报告只读诊断模型（`status_report.go` & `behavior_status_report.go`）

新增只读诊断结构 `BoundReviewDiagnostic`，同时嵌入 `RestoreStatus`（`backup status`）与 `BehaviorLedgerDiagnostic`（`backup behavior-status`）的 `bound_behavior_review` 字段：

- **字段定义**：
  - `present`（`bool`）：磁盘是否存在该文件（仅 `ErrNotFound` 为 `false`，读取/关闭/权限错误不得伪装为不存在）。
  - `status`：`absent`、`unavailable`、`changed`、`invalid`、`inconsistent`、`drifted`、`expired`、`partial`、`complete_unregistered`、`registration_pending`、`complete_registered`。
  - `reason`：脱敏原因码（如 `bound_review_absent`、`bound_review_malformed`、`bound_review_unavailable`、`bound_review_checks_incomplete`、`bound_review_expired`、`operation_changed_since_report`、`operation_changed_since_registration`、`partial_bound_review_registered`、`ledger_binding_mismatch` 等）。
  - `profile`、`report_status`（`partial`/`complete`）、`freshness`（`not_observed`/`fresh`/`not_yet_valid`/`expired`）。
  - `control_binding`（`not_observed`/`matches_current_control`/`matches_registration_base`/`stale_generation_or_state`/`control_mismatch`/`ledger_mismatch`）、`observed_generation`、`current_generation`。
  - `pending_checks`（`status == "unavailable"` 的检查 ID 列表）与 `failed_checks`（若存在异常 `fail`）。
  - `registration_state`、`registration_binding`（`absent`/`not_applicable`/`bound_base_observed`/`bound_next_observed`/`drifted`/`binding_mismatch`/`other_review_registered`）。
  - `persistence_confirmation` 恒为 `"not_inferred"`；`execution_authorized`、`ready_for_cutover`、`deployment_authorized` 恒为 `false`。
- **双命令语义分工**：
  - `backup status`（`status_report.go` + `status.go`）：在 `readStatusSnapshot` 中以双次稳定读取纳入 `behavior-account-review.json`；当 `handover-registration.json` 的 `scope == "bound-behavior-review-registration"` 或仅有通用报告存在时，`RestoreStatus` 直接依据通用报告与登记链判定 `observed_registered` / `registration_pending` / `review_only` / `blocked` / `inconsistent`（若 `partial` 报告被意外写入登记或报告缺失，一律判为 `inconsistent`）。保留现有仅有 `handover-review.json` 时的全部状态与退出码语义不变。
  - `backup behavior-status`（`behavior_status_report.go` + `behavior_ledger.go`）：保持 `stableBehaviorLedger` 双次动作链读取不变，在其后以双次控制+报告+登记快照核验控制记录一致性与通用报告绑定（含 `ConsentID`、`Ledger` 摘要、`AccountCount` 及 `Identity.Runtime` 与 `behavior-intent.json` 对齐）；任一读漂移立即返回 `changed`。

## 3. CLI 输出与退出码（`cmd/backup/main.go` & `cmd/backup/behavior_status.go`）

- `backup status`：`json` 输出含 `bound_behavior_review`；`text` 输出明确展示通用报告存在性、状态、待验项清单、时效、控制绑定与版本、登记绑定及“持久确认: 无法从磁盘推断 / 允许切换/部署: 否”。退出码仅在 `status == "observed_registered"` 时为 `0`，其余需要人工核对的状态（含 `partial`、`review_only`、`registration_pending`、`blocked`、`inconsistent`、`expired`、`drifted`）返回 `4`。
- `backup behavior-status`：`json` 输出含 `bound_behavior_review`；`text` 输出明确展示通用报告存在性、状态、待验项清单、时效、控制绑定与版本，以及“数据库撤销: 未核验 / 历史持久确认: 无法推断 / 允许派发/重试/部署: 否”。退出码仅在 `status == "responses_observed"` 且当 `bound_behavior_review.present == true` 时其状态为 `complete_unregistered` 或 `complete_registered` 才返回 `0`，否则返回 `4`。

## 4. 通用完整报告 CAS 登记门禁（`handover_registration.go` & `behavior_review.go`）

1. **严格完备性校验（`boundBehaviorReview.validateBindingsFor` / `validateCompleteFor`）**：
   - `validateCompleteFor(p, s, live, now)` 强制要求 `Status == "complete"`、全部 12 项检查均为 `"pass"`（`partial` 或任一 `unavailable`/`fail` 一律返回 `errBehaviorReview`）、三控制记录与计划/存储/目录树/目标三身份及窗口新鲜度严格匹配。
2. **CAS 状态机与幂等保护（`registerBoundBehaviorReview`）**：
   - 复用共享 CAS 登记核心 `registerReviewCAS`，在 `handoverRegistration.validate()` 中允许 `scope == "bound-behavior-review-registration"`（与旧 `"isolated-review-registration"` 严格区分 `ReportSHA` 归属）。
   - 仅当 `report.validateCompleteFor` 与磁盘重读 `behavior-account-review.json` 的 `ReportSHA` 一致且未过期时：创建 `handover-registration.json`（`state: "prepared"`）-> `CompareAndSwap(state.json)` 递增 `generation` 并保持 `phase == "validating"` -> 双重确认后 `CompareAndSwap(handover-registration.json)` 置为 `"registered"`。
   - `CreateOnly` 或 `CompareAndSwap` 冲突返回 `storage.ErrConflict`；任何确认丢失或回读不一致返回 `errRegistrationUnknown`，保留既有文件并禁止重放。
3. **Live Guard 登记入口（`restoreGuard.registerBoundReview`）**：
   - 在 `confirmedBehaviorRun` 中增加 `reviewPersisted bool`（仅在 `persistBoundBehaviorReview` 完整确认落盘及前后账本一致后置 `true`），并与 `g.registrationStarted` 互斥锁存。
   - 若 `persistBoundBehaviorReview` 确认丢失，即使磁盘上可见 `behavior-account-review.json`，`registerBoundReview` 亦必须在触发任何目标探针前拒绝（返回 `errRegistrationUnknown`）。
   - 登记前重新执行 `inputs.probes(p)`（`Database`/`Runtime`/`Redis`）、`probeHandoverDataSessions`（对象树/Header/会话零活跃）与 `recheckBindingsLocked`，确保同次真实目标状态未发生漂移。

## 5. 验证计划

1. `python scripts/verify-backup-contract.py`：65 项契约检查与 16 份冻结摘要不变。
2. `handover_registration_test.go`、`status_test.go`、`behavior_ledger_test.go` 及 `cmd/backup` 单测：覆盖通用报告完整登记链（`validating` 不变、`generation` 递增）、部分报告（`10/2`、`11/1`）/过期/篡改拒绝、持久化确认丢失不可从磁盘恢复登记权、CAS 确认丢失保留不重放、`backup status` 与 `backup behavior-status` 对 `absent`/`partial`/`complete_unregistered`/`prepared`/`registered`/`expired`/`drifted`/`inconsistent`/`invalid`/`unavailable`/`changed` 的 JSON/text 与退出码校验。
3. `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 与 `go vet -mod=readonly -tags=integration ./...`。
4. 独立无网络 Linux 容器 CLI 验证：`python scripts/verify-backup-status-cli.py` 与 `python scripts/verify-backup-behavior-status-cli.py`。
5. Linux 隔离 PostgreSQL 16.15 + Redis + 真实归档/TLS 联合验收：`python scripts/verify-backup-restore-db.py --binary .toolchain/backup-bound-registration-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`（含真实 `dispatch_http_good` 完整通用报告的 CAS 登记与不可重放断言）。
