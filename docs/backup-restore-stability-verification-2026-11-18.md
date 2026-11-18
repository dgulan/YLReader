# 备份恢复间歇失败定位与排他同步验收（2026-11-18）

先行设计见 [备份恢复间歇失败诊断与连接排他同步设计](backup-restore-stability-design-2026-11-18.md)，上轮遗留问题见 [Gemini 审计修复验收](gemini-audit-remediation-verification-2026-11-17.md)。本轮定位并修复 `StageRecorded` 计划门禁、`PublishObjects` 对象发布及 `dispatch_intent_ack` / `headerhttp` 账号核验在隔离集成测试中的间歇性 `pg_stat_activity` 排他误判问题，补充最小脱敏阶段诊断，未放宽恢复门禁、排他检查或超时，未开放生产恢复执行或切换入口。

## 根因定位与诊断实证

1. **历史失败产物复核（`.toolchain/backup-restore-db-fb8da7261774/`）**：
   - `TestRestoreHeaderIntegration/headeraad` 在 `PreparePlan` 通过后，于 `StageRecorded` 调用的 `g.Recheck` -> `PostgresTargetInspector.CheckEmpty` 返回 `ErrPlan`。
   - `TestRestoreHeaderIntegration/dispatch_intent_ack/dispatch_run` 仅耗时 `0.02s` 即返回 `errBehaviorDispatch`（正常执行两位合成用户 Argon2id 密码核验需 `0.3s–0.6s`）。代码路径复核确认：`g.dispatchBehaviorOnce` 先调用 `g.verifyBehaviorAccounts`，通过后才调用 `dispatchBehaviorCore` -> `recordBehaviorIntent` 创建 `behavior-intent.json`。因前置 `verifyBehaviorAccounts` 在 `0.02s` 内（进入 Argon2id 前）失败返回，磁盘上从未写入 `behavior-intent.json`，而原测试仅检查 `e == errBehaviorDispatch`，导致后续 `dispatch_diagnostic`、`ledger_response_claims`、`ledger_database_observation` 因读到 `intent_not_observed` / `unavailable` 连锁报错。
2. **新增脱敏阶段诊断后的实证捕获（`.toolchain/backup-restore-db-f6713c4fa7a0/` 与 `.toolchain/backup-restore-db-b83275d1c435/`）**：
   - **机制一（客户端连接关闭异步竞争 `PgConn.Close` vs `pg_stat_activity`）**：`pgx/v5` 的 `PgConn.Close(ctx)` 在向套接字写入 5 字节 `Terminate`（`'X', 0, 0, 0, 4`）后立即关闭客户端套接字返回，不等待 PostgreSQL backend 进程执行 `proc_exit_prepare` -> `shmem_exit`（`pgstat_beshutdown_hook` / `ProcKill`）。在 `ParseTarget` 中通过 `AfterNetConnect` 注入 `syncTerminateConn`，在发送 `Terminate` 后执行 `CloseWrite()` 并等待服务端 `EOF`（上限 2s），确保前序客户端连接在 `Close()` 返回前已完成 `shmem_exit`。
   - **机制二（PostgreSQL 内置 `autovacuum worker` 与非特权角色 `backend_type` 权限掩码）**：
     - 在启用 `syncTerminateConn` 后，诊断运行 `.toolchain/backup-restore-db-f6713c4fa7a0/` 在容器启动约 65s（跨越 PostgreSQL 默认 `autovacuum_naptime = 60s`）时，于 `dispatch_http_lost` 的 `PublishObjects` -> `validatePublicationDatabaseSessions` 捕获到 `ErrTargetDenied`。
     - 诊断运行 `.toolchain/backup-restore-db-b83275d1c435/` 进一步在 `headerhttp/restored_accounts_valid`（耗时 `0.05s`）复现证实：备份恢复的 6 处 `pg_stat_activity` 排他查询均以非特权、无角色成员关系的隔离属主角色 `reader` 执行；PostgreSQL 内置 `autovacuum worker` 的 `st_userid` 为 `InvalidOid`（0，`usesysid IS NULL`），在 `pg_stat_get_activity` 中 `HAS_PGSTAT_PERMISSIONS(beentry->st_userid)` 对非特权角色返回 `false`，导致 `backend_type` 被掩码为 `NULL`（而 `datid`、`pid`、`usesysid` 在权限分支外无条件暴露）。
     - 在 6 处 `pg_stat_activity` 排他子查询中统一使用 `AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker'`，在非特权角色 `reader` 与超级用户视角下一致排除无会话用户的内置 `autovacuum worker`，同时继续拦截任何角色（含 `reader`、`postgres` 超级用户及其他角色）的其他客户端连接或带角色后台进程。

## 本机与容器内单测

- Windows 本机单元测试：`go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 与 `go vet -mod=readonly -tags=integration ./...` 全部通过（0 失败）。
- Linux 无网络只读容器全量非 DB 单测：在 `postgres:16` 镜像、`--network none --read-only --user 10001:10001` 与私有 `tmpfs` 下运行 `.toolchain/backup-restore-stability-linux.test -test.v=true`，全部通过（仅需专用外部夹具的集成入口按设计 skip），日志 `.toolchain/backup-restore-stability-linux-unit.log`。新增 `TestSyncTerminateConnWaitsForPeerEOF` 与 `TestPlanStageErrorRedactionAndUnwrap` 通过，并补齐 `TestHandoverProbeFactoryRejectsOtherRuntimeTarget` 在容器内置 `PG_MAJOR`/`PGDATA` 环境变量下的 `cleanPG(t)` 隔离。

## 真实归档/数据库隔离联合链（连续两轮完整通过）

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-restore-stability-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

连续执行两轮全新隔离集群验证：

1. **第一轮 `.toolchain/backup-restore-db-1782f1b705b0/result.json`**：`passed=true`，**127 个通过事件**，0 失败/跳过，4 个自有临时容器 `cleanup` 全部 `true`。
2. **第二轮 `.toolchain/backup-restore-db-65f779da01af/result.json`**：`passed=true`，**127 个通过事件**，0 失败/跳过，4 个自有临时容器 `cleanup` 全部 `true`。

另有启用 `syncTerminateConn` 后的首轮隔离验证 `.toolchain/backup-restore-db-86ca7d6533d4/result.json` 同样 `passed=true`（127 个通过事件）。

| 证据 | SHA-256 / 路径 |
|---|---|
| 最终 Linux 备份测试二进制 | `ffec401b0c98355b0733672b06dc039ab4398b226358899479841bda828b66f4` (`.toolchain/backup-restore-stability-linux.test`) |
| 合成数据库归档 | `3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303` |
| PostgreSQL 16.15 镜像 | `f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657` |
| Redis 镜像 | `c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b` |

全部历史与本轮失败诊断产物完整保留，未删除或覆盖：

- `.toolchain/backup-restore-db-f8fcdbcbcd45/`（上轮初始缺 `orphan` 建库清单失败记录）
- `.toolchain/backup-restore-db-fb8da7261774/`（上轮 `headeraad` 与 `dispatch_intent_ack` 间歇失败记录）
- `.toolchain/backup-restore-db-f6713c4fa7a0/`（本轮启用 `syncTerminateConn` 后捕获 `dispatch_http_lost` 在 `PublishObjects` 遭遇 `autovacuum worker` 的诊断记录）
- `.toolchain/backup-restore-db-b83275d1c435/`（本轮捕获非特权角色 `reader` 下 `autovacuum worker` 的 `backend_type` 被掩码为 `NULL` 导致 `headerhttp/restored_accounts_valid` 失败的诊断记录）

## 契约与未决边界

- `python scripts/verify-backup-contract.py`：65 项契约检查全部通过，16 份冻结契约文件 SHA-256 摘要完全匹配（报告 `.toolchain/backup-contract-checks.json`）。
- `gofmt` 与 `git diff --check` 通过。未访问 `references\` 或任何生产数据库，未执行提交、推送、合并或部署。
- 剩余边界：虽然连续两轮完整 127 事件隔离恢复运行均 100% 通过，且双层竞争根因（客户端 `Terminate` 关闭同步 + 非特权角色下 `autovacuum worker` 过滤）均已实证闭合，但在极端宿主机 I/O 停顿下 `syncTerminateConn` 的 2s `EOF` 读取上限以及完整交接链后续待办（旧 refresh 令牌只读/受控核验、同次证据绑定与阅读器回路、生产执行 CLI）仍需按后续阶段计划继续推进。
