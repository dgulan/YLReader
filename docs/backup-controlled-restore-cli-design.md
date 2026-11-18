# 受控恢复执行 CLI 入口与操作员交接闭环设计（H5）

状态：H5 先行设计。严禁修改 43 个公开 API、`docs/schema.sql`、备份归档格式或 16 份冻结契约摘要；不注册任何 Web Server 路由，不自动启动常驻业务进程或切换生产流量；即使完成数据库恢复、对象发布或完整行为审阅 CAS 登记，`state.json` 的 `phase` 仍保持 `validating`，且 `execution_authorized`、`ready_for_cutover` 与 `deployment_authorized` 恒为 `false`。

## 1. 命令分离原则（`backend/cmd/backup/main.go`）

`backup` CLI 严格隔离两类入口，禁止相互混用参数或派生权限：

- **只读预览与诊断子命令**：`inspect`、`plan`、`status`、`behavior-status`。不获取恢复锁，不修改目标数据库或目标存储；若在只读命令中传入执行专有参数（如 `--plan-digest`、`--confirm-target`、`--owner`、`--pg-restore`、`--header-keys`、`--timeout`），立即返回退出码 `2`。
- **受控恢复执行子命令**：`restore`。仅响应显式 `restore` 子命令与完整操作员授权参数集合；绝不读取环境变量以派生执行权，也不直接消费 `inspect`/`plan` 预览的运行上下文。

## 2. 多参数操作员授权与输入校验（`backend/cmd/backup/restore.go`）

`backup restore` 必须显式提供并同时通过以下参数与一致性校验，缺失、重复、非法字符、相对路径或参数越界均在打开任何目录/数据库前以退出码 `2` 拒绝：

- `--input-root ABSOLUTE_PATH`：安全输入目录根路径（通过 `storage.OpenBackupFiles` 打开，防符号链接与路径穿越）。
- `--request-key KEY`：`input-root` 内受限相对键，读取并严格校验 `InspectionRequest`。
- `--config-key KEY`：`input-root` 内受限相对键，读取并严格校验 `Config`；随后强制校验 `backup.CheckInspectionIdentity(inputRoot, request, config)` 与 `request.Limits == config.Limits`。
- `--plan-key KEY`：`input-root` 内受限相对键，读取既有冻结 `Plan` JSON（拒绝现场重新生成或隐式替换计划）。
- `--plan-digest SHA256`：64 位小写十六进制计划摘要，必须与已加载且通过 `Plan.Validate()` 校验的 `plan.Digest` 完全一致。
- `--confirm-target UUID`：规范小写 UUID，必须同时等于 `plan.TargetID` 与 `config.Target.InstanceID`。
- `--owner UUID`：操作员属主 UUID，必须为有效规范非零 UUID。
- `--header-keys ABSOLUTE_PATH`：加密字段密钥环绝对路径，经 `backup.LoadRestoreHeaderKeyring` 读取并严格校验（`0600` / 非符号链接 / 精确 JSON / strict base64）。
- `--pg-restore ABSOLUTE_PATH`：可信 `pg_restore` 可执行文件绝对干净路径，传入 `PostgresArchiveTool`（子进程封闭环境，禁用 `PATH`/`PG*`）。
- `--timeout DURATION`：显式受控超时（合法 Go `time.Duration`，严格限定 `1s <= timeout <= 30m`），通过 `context.WithTimeout` 绑定全链生命周期。
- 可选 `--handover-key KEY`：`input-root` 内受限相对键，显式指定交接审阅/行为闭环配置（`ylreader-handover-execution-spec` v1）；密钥文件路径须符合 `safeAbsolute` 并通过 `storage.ReadBackupSecret` 读取、`defer clear()` 擦除，禁止 JSON 内联明文密钥。
- 可选 `--format json|text`：默认 `json`。

## 3. 全链门禁复用与失败不重试/不删保留状态机（`backend/internal/backup/controlled_restore.go`）

由 `RunControlledRestoreFromInput` 统一协调并在单次 `restoreGuard` 生命周期内顺序推进：

1. **包与冻结计划复核**：打开临时根目录与安全输入包，解析 `.age` 密钥并执行 `VerifyPackage`，随后执行 `CheckPlan(..., PostgresTargetInspector{})` 与 `acquireRestoreGuard`。
2. **阶段与不可重放控制**：
   - 调用 `g.StageRecorded(ctx, pkg, ownerID, confirmTarget, now, PostgresTargetInspector{})`。若控制目录已存在同 `OperationID` 记录（`stage == nil`），立即终止且不修改任何状态，返回 `RESTORE_ALREADY_RECORDED`。
   - 调用 `g.RunDatabaseWithHeaders(ctx, stage, ownerID, confirmTarget, now, tool, config.Limits.Plaintext, ring)` 完成带事务 Header 密文校验的隔离库恢复与提交。
   - 调用 `g.PublishObjects(ctx, stage, ownerID, confirmTarget, now)` 完成阶段性发布与 `publication.json`（`verified`）持久化。
3. **可选交接审阅前置采集（仅在显式提供 `--handover-key` 时）**：
   - `mode == "partial_review"`：构造真实目标探针（数据库/Redis/运行时），收集并持久化部分 `handover-review.json`（8 pass / 2 unavailable），确认部分报告无法进入 CAS 登记，以 `RESTORE_HANDOVER_PARTIAL_UNREGISTERED` 结束并返回退出码 `4`；`bound_behavior_register` 的完整 12/12 行为验证与 CAS 登记须在对象发布后基于真实的 `[OperationSHA, AttemptSHA, PublicationSHA]` 同意链执行（见独立全链路集成测试）。
4. **不确定结果与取消/超时的安全边界**：
   - 凡遇 `ErrRestoreCommitUnknown`、`ErrPublication`、`errHandoverUnconfirmed`、`errRegistrationUnknown`、`storage.ErrConflict`、`context.Canceled` 或 `context.DeadlineExceeded`：
     - **零重试**：禁止任何自动重新执行写入、补写或重试 HTTP/CAS 操作；
     - **零清理**：禁止自动删除 `staging`/`pending`/`output` 对象及控制文件；
     - **脱敏输出**：仅输出 `ControlledRestoreResult`（含 `status_code`、`operation_phase`、`outcome`、`database_outcome`、`publication_state`、`handover_review_status`、`registration_state`、`artifacts_retained`、`retry_safe: false`），绝不泄露 DSN、文件路径、密钥或原始错误文本；`execution_authorized`、`ready_for_cutover`、`deployment_authorized` 恒为 `false`。

### 状态码与退出码契约

| `status_code` | 含义 | `retry_safe` | `exit_code` |
|---|---|---|---|
| `RESTORE_VALIDATING_PUBLISHED` | 未请求交接闭环，库+Header+对象发布已校验并停留在 `validating` | `false` | `0` |
| `RESTORE_VALIDATING_REGISTERED` | 含 12/12 完整通用审阅与 CAS 登记，停留在 `validating` | `false` | `0` |
| `RESTORE_HANDOVER_PARTIAL_UNREGISTERED` | 部分审阅已保存，按规则拒绝 CAS 登记 | `false` | `4` |
| `RESTORE_ALREADY_RECORDED` | 同 `OperationID` 已存在控制记录，拒绝重放并保留现场 | `false` | `4` |
| `RESTORE_REJECTED` | 包认证、计划摘要/时效、目标空库或 Header 核验拒绝 | `false` | `4` |
| `RESTORE_CANCELED` | 执行前或过程中收到取消/超时（未落入 commit unknown） | `false` | `4` |
| `RESTORE_COMMIT_UNKNOWN` | 提交确认丢失（保留全部对象与记录，禁止重放） | `false` | `4` |
| `RESTORE_PUBLICATION_FAILED` | 对象发布失败或确认丢失（保留暂存/发布目录，禁止删改） | `false` | `4` |
| `RESTORE_HANDOVER_UNCONFIRMED` | 审阅收集/持久化确认丢失（保留既有关联记录） | `false` | `4` |
| `RESTORE_REGISTRATION_UNKNOWN` | CAS 登记冲突或确认丢失（保留既有文件，禁止重放） | `false` | `4` |

另约定通用进程退出码：`2` = 参数/格式/安全校验失败（不触碰目录或库）；`3` = 输入目录打开或运行前置不可用；`5` = 安全目录关闭确认失败或标准输出写失败。

## 4. 验证计划

1. `python scripts/verify-backup-contract.py`：确认 65 项契约检查与 16 份冻结契约 SHA-256 摘要不变。
2. 单元测试：`backend/internal/backup/controlled_restore_test.go` 与 `backend/cmd/backup/restore_test.go` 覆盖参数缺失/越权、只读命令隔离、`plan-digest`/`confirm-target`/重放拒绝、以及 `ErrRestoreCommitUnknown`/`ErrPublication`/`errRegistrationUnknown` 零重试零泄露验证；运行 `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 与 `go vet -mod=readonly -tags=integration ./...`。
3. 独立预览回归：`python scripts/verify-backup-preview-cli.py`（验证已有 `inspect`/`plan` 行为零退化）。
4. 独立受控恢复 CLI 容器验证：`backend/cmd/backup/restore_cli_linux_test.go` + `scripts/verify-backup-restore-cli.py`，在无外部网络的隔离 PostgreSQL 16 + Redis 容器内执行真实 `/verify/backup-cli restore` 进程测试：验证拒绝不足参数、篡改摘要、非空库拒绝、核心恢复+发布闭环（`RESTORE_VALIDATING_PUBLISHED`）及完整交接+行为审阅+CAS 登记闭环（`RESTORE_VALIDATING_REGISTERED` -> `backup status` exit `0`）与重放拒绝。
5. 全量隔离 DB 联合验收：`python scripts/verify-backup-restore-db.py --binary .toolchain/backup-restore-cli-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review`。
