# 备份恢复间歇失败诊断与连接排他同步设计（2026-11-18）

状态：内部诊断与连接收尾同步修复设计。不更改冻结的 43 个公开 API、`docs/schema.sql`、备份归档格式或 16 份冻结契约摘要；不放宽恢复门禁、排他检查或超时，不开放生产执行/切换入口。

## 1. 失败证据与现象复核

基于保留的失败产物 `.toolchain/backup-restore-db-fb8da7261774/tests.txt` 与成功复跑产物 `.toolchain/backup-restore-db-06a02914866a/tests.txt`（同一测试二进制 SHA-256 `04c04bcb1cfb41f410d2a8de445b07fd5885646552b903e8192f1275270b1016`）比对：

1. **`TestRestoreHeaderIntegration/headeraad` 计划门禁失败（`0.51s`）**：
   - 在 `PreparePlan` 只读检查通过后，紧随其后的守卫复核（`acquireRestoreGuard` / `StageRecorded` 调用的 `g.Recheck` -> `PostgresTargetInspector.CheckEmpty`）返回 `ErrPlan`（`backup: invalid, expired or changed restoration plan`）。
   - 该目标库 `yreader_a1_iso_headeraad` 仅由当前子测试独占访问，静态角色、属主、空库状态与配置指纹在 `PreparePlan` 与复核之间未发生任何外部变更。
2. **`TestRestoreHeaderIntegration/dispatch_intent_ack` 意向与账本诊断失败**：
   - `dispatch_intent_ack/dispatch_run` 仅耗时 **`0.02s`** 即返回 `errBehaviorDispatch`（对比正常执行 2 个账号 Argon2id 密码校验的 `06a02914866a` 耗时 `0.52s`）。
   - 随后 `dispatch_diagnostic` 失败于 `disk inferred retry`、`ledger_response_claims` 失败于 `wrong ledger observation`、`ledger_database_observation` 失败于 `wrong database observation: unavailable`。
   - 追踪代码路径可知：`g.dispatchBehaviorOnce` 先调用 `g.verifyBehaviorAccounts`，成功后才进入 `dispatchBehaviorCore` -> `recordBehaviorIntent` 写入 `behavior-intent.json`。由于 `verifyBehaviorAccounts` 在 `0.02s` 内（进入 Argon2id 密码校验前）提前失败，`dispatchBehaviorOnce` 同样返回 `errBehaviorDispatch`；`dispatch_run` 仅检查 `e == errBehaviorDispatch` 而假性通过，此时磁盘上根本未创建 `behavior-intent.json`，导致后续三个诊断子测试读到 `intent_not_observed` / `unavailable`。

## 2. 最小脱敏阶段诊断设计

为在不改变任何公开或内部哨兵错误语义、且绝不泄露 DSN/路径/凭据/SQL 文本的前提下精确区分失败阶段：

1. **计划与空库复核脱敏阶段标记（`ErrPlan`）**：
   - 引入内部 `planStageError(stage string) error`，以固定 ASCII 阶段码包装 `ErrPlan`（保持 `errors.Is(err, ErrPlan) == true`）。
   - 区分阶段码：`plan_fields`、`location_fingerprint`、`target_connect`、`target_tx_begin`、`target_query`、`target_role_or_owner`、`target_not_pristine`、`target_not_exclusive`、`target_tx_commit`。
2. **一次性行为派发与前置账号核验脱敏阶段标记**：
   - 在不可序列化的内部结果 `behaviorDispatchObservation` 上增加脱敏字段 `Stage string`，在 `restoreGuard` 上记录最近一次前置账号核验子阶段 `accountStage string`（仅取固定常量值）。
   - 派发阶段固定枚举：`guard_preflight`、`account_verification:<substage>`（含 `probes_init`、`handover_database_probe`、`handover_runtime_probe`、`handover_redis_probe`、`handover_data_probe`、`snapshot_binding`、`recheck_bindings`、`account_probe_connect`、`account_probe_exclusive`、`account_probe_credentials`、`post_recheck`）、`pre_intent_recheck`、`intent_record`、`dispatch_action_persist`、`login_transport`、`post_login_persist_or_logout`、`completed`。
   - 保持 `errBehaviorDispatch`、`errBehaviorAccounts`、`errHandoverReview` 等哨兵错误指针完全不变，兼容全部既有单测直接相等判断。
   - 在集成测试 `dispatch_run` 中显式断言故障模式对应的目标阶段（如 `intent_ack` 必须到达 `intent_record`），并在诊断断言失败时输出脱敏的 `Status`/`Reason`/`Stage`。

## 3. 根因分析与 PostgreSQL 连接收尾同步修复

### 3.1 根因机制（双重竞争叠加）

1. **客户端连接关闭异步竞争（`PgConn.Close` vs `pg_stat_activity`）**：
   - `github.com/jackc/pgx/v5/pgconn` 的 `PgConn.Close(ctx)` 在向套接字写入 5 字节 `Terminate`（`'X', 0, 0, 0, 4`）后立即调用客户端 `net.Conn.Close()` 返回，**不等待 PostgreSQL 服务端 backend 进程处理 `'X'` 并退出**。
   - 在 PostgreSQL 服务端（`proc_exit` -> `proc_exit_prepare`），`shmem_exit()`（执行 `pgstat_beshutdown_hook` 与 `ProcKill()` 从 `pg_stat_activity` 移除当前条目）先于 `on_proc_exit` 关闭服务端套接字执行。若客户端不等待服务端 `EOF` 即立刻发起下一条连接查询 `pg_stat_activity`，在高 CPU 负载下刚关闭的前一条客户端连接可能尚未执行完 `shmem_exit()`。
2. **PostgreSQL 内置 `autovacuum worker` 误判竞争与非特权角色 `backend_type` 掩码（由诊断运行 `.toolchain/backup-restore-db-f6713c4fa7a0` 与 `.toolchain/backup-restore-db-b83275d1c435` 实证确认）**：
   - 在接入 `syncTerminateConn` 确保前序客户端连接均已收到服务端 `EOF` 后，诊断运行 `.toolchain/backup-restore-db-f6713c4fa7a0` 在容器运行约 65s（跨过 PostgreSQL 默认 `autovacuum_naptime = 60s`）时，于 `TestRestoreHeaderIntegration/dispatch_http_lost` 的 `PublishObjects` -> `validatePublicationDatabaseSessions` 捕获到 `ErrTargetDenied`。
   - 进一步在 `.toolchain/backup-restore-db-b83275d1c435` 中验证发现：由于备份恢复的 6 处 `pg_stat_activity` 排他性查询均以非特权、无角色成员关系的隔离属主角色 `reader` 执行，而 PostgreSQL 内置 `autovacuum worker` 在 `BackendStatusArray` 中的 `st_userid` 为 `InvalidOid`（0，`usesysid IS NULL`），在 `pg_stat_get_activity`（`src/backend/utils/adt/pgstatfuncs.c`）中 `HAS_PGSTAT_PERMISSIONS(beentry->st_userid)` 对非特权角色返回 `false`，导致 `reader` 查询到的 `autovacuum worker` 行的 `backend_type` 列被掩码为 `NULL`（而 `datid`、`pid`、`usesysid` 在权限分支外无条件暴露）。与此相对，任何已认证并附着到目标数据库的会话（无论 `reader`、`postgres` 超级用户或其他角色）其 `usesysid` 均非空（`OidIsValid(st_userid) == true`）。
   - 因此，仅判断 `COALESCE(backend_type,'client backend')<>'autovacuum worker'` 在非特权角色 `reader` 下会将 `backend_type = NULL` 的 `autovacuum worker` 误归类为 `'client backend'`；必须同时结合无权限掩码的 `usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker'`，才能在非特权角色与超级用户视角下一致排除无会话用户的内置 `autovacuum worker`，同时继续拦截任何角色（含超级用户或其他角色）的客户端及带角色后台会话。

### 3.2 修复原则与实现

- **不放宽任何安全与排他门禁**：保留全部 6 处 `pg_stat_activity` 排他性检查，继续拒绝任何角色（含 `reader`、`postgres` 超级用户及其他角色）的其他 `client backend`、并行查询 worker、扩展后台 worker 及复制会话；不添加查询重试/sleep，不调大测试超时，不跳过任何测试。
- **过滤内置 `autovacuum worker` 并补充发布检查脱敏阶段码**：
  - 在 `inspect.go`、`restore_database.go`、`restore_observation.go`、`restore_publication.go`、`database_identity.go`、`behavior_accounts.go` 共 6 处 `pg_stat_activity` 排他子查询中增加 `AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker'`。
  - 在 `restore_publication.go` 的 `validatePublicationDatabaseSessions` 中使用 `targetDeniedStageError` 包装 `ErrTargetDenied`（保持 `errors.Is(err, ErrTargetDenied) == true`），区分 `publication_db_query`、`publication_db_role_or_owner`、`publication_db_not_exclusive` 与 `publication_db_version`。
- **连接关闭与服务端退出同步（`syncTerminateConn`）**：
  - 在 `ParseTarget`（`backend/internal/backup/target.go`）构造的 `pgx.ConnConfig`（含 `Fallbacks`）中设置 `AfterNetConnect`，将底层 `net.Conn` 包装为 `syncTerminateConn`。
  - 当检测到连接已写出 PostgreSQL `Terminate` 报文（`'X', 0, 0, 0, 4`）并在随后执行 `Close()` 时，先调用底层连接的 `CloseWrite()`（若支持），并在有界短截止时间（2s）内读取服务端直至 `EOF`，随后再关闭客户端套接字。
  - 在备份恢复集成测试中，将直接调用 `pgxpool.New(ctx, raw)` 的辅助池统一通过 `ParseTarget` 的连接配置创建，确保 `pool.Close()` 销毁空闲连接时同样等待服务端 backend 完成退出。
