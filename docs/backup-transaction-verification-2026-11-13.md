# 隔离数据库恢复事务验证（2026-11-13）

本轮完成数据库恢复的内部事务原语 restoreIsolatedDatabase，以及选定表的 COPY 数据通道。没有新增 API 或 CLI apply 入口，没有操作业务实例、提交、推送、合并或部署。这里的成功只代表数据库子步骤，不代表完整备份恢复成功。

## 实现

1. 目标必须经过现有 ParseTarget：显式 URI、回环地址、独立数据库名称白名单、显式凭据且没有 PG 环境覆盖。调用方必须预先提供独立隔离集群中的新空库；原语不创建、删除或清理配置指定的数据库。
2. 整个过程持有私有归档读取句柄，防止间隙清理。先完成包内数据库目录和全部数据段预检，再连接目标，防止未完成预检就写入数据库。
3. 在同一目标连接和 Serializable 事务内获取与迁移共用的事务 advisory lock，重查数据库所有权、非特权角色、空 public/扩展/schema 及无其他数据库连接；限制目标 PostgreSQL 为 16。设置本地 search_path、锁等待与语句超时；整个操作最多 3 分钟。
4. 新增 migration.UpInTransaction 复用已嵌入的可信基线，不自行提交。既有 migration.Up 保持自己开启/提交事务的行为。基线、扩展、账本种子、种子删除和归档账本加载均包含在外层事务中；没有改变历史迁移 SQL 或其摘要。
5. 按冻结依赖顺序加载 14 张表。每表重新打开私有归档，以固定 `--schema=public --table=<已知表名>` 调用受信 pg_restore，只展开该表。选表参数使用未限定表名配合独立 schema 参数；`--table=public.chapters` 在本工具版本下不会选中该项，实验发现后已改正，空输出不能被校验器当作成功。
6. 选定表解析器校验固定 prologue、精确 COPY 列清单、行宽/计数和配对结束标记。只把 COPY 行写入 io.Pipe，由 pgconn.CopyFrom 使用源码内固定的 COPY 语句送入协议；prologue、归档 SQL、序列 SQL 和 psql 元命令不进入执行通道。官方 [pgconn CopyFrom 文档](https://pkg.go.dev/github.com/jackc/pgx/v5/pgconn#PgConn.CopyFrom) 说明了该原始 COPY 协议接口。工具异常、数据错误或数据库拒绝均取消生产者、关闭管道并等待子进程回收。
7. 将预检得到的两个序列值通过参数化 setval 应用于本事务新建的序列。核对迁移账本及数据库中的 14 表实际计数，让数据库 COPY/约束执行类型与外键验证。没有禁用触发器、外键、约束或调整 session_replication_role。测试夹具的异常源由专用超级用户在临时集群中构造，恢复角色仍无超级用户权限。
8. 同一事务内撤销尚未撤销的会话、递增全部用户 token_version，将 queued/running 任务置 canceled、清空 worker_id、设置 finished_at；报告变更行数。用户 updated_at 会按可信基线触发器随安全版本更新，不承诺这类预期变更字段逐字节不变。
9. 提交前检查未观察到其他目标连接。失败使用独立、最长 5 秒的清理上下文回滚；不能确认回滚时返回专用错误。提交调用失败且不是明确的事务回滚时返回 CommitUnknown，不自动重试，也不声称目标为空。

advisory lock 只排除配合此约定的迁移/恢复进程。连接观察不是强制隔离机制，不能阻止管理员或不合作客户端在检查后进入；独立集群与外层操作隔离仍是必要前提。本轮没有把只读计划缓存作为写入授权，也没有接入 plan_digest、控制状态或对象目录锁；这些必须由后续整体 apply 编排持有和重验，当前原语保持包内不可导出。

选表方式每次重新读取归档，避免为了 TOC 重排把任意宿主路径交给外部工具；它不持久化展开 SQL，也不缓冲整表。大包重复扫描成本尚未压测，不能据合成样本承诺容量或吞吐。

## 真实数据库验证

脚本 scripts/verify-backup-restore-db.py 创建唯一命名、唯一标签的 PostgreSQL 容器，network=none、无宿主端口/宿主数据挂载，数据库数据位于 tmpfs。测试容器共享该隔离网络命名空间，只能通过本地回环访问专属数据库；测试程序、源码、docs、合成归档均只读挂载，以 UID 10001 运行，明文暂存仅在临时 tmpfs 中。finally 按本轮标签确认所有权后清理容器及匿名卷，不使用业务配置或命名卷。

通过记录：`.toolchain/backup-restore-db-14fa1c1d5bea/result.json` 与 tests.txt，PostgreSQL 16.15，镜像 ID f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657。1 个顶层、6 个子测试，共 7 个通过事件，0 失败/跳过；两个自有容器清理均为 true。该测试二进制未启用 race，race 结果单独列于下节。

- 有效合成包恢复成功：14 表 21 行；阅读进度 char_offset=17、version=7、server_updated_at 固定时间保持；中文及 emoji 书签保持；2 个会话撤销、2 个用户令牌版本从 3 变 4、2 个任务终止；两个序列值及 called 状态匹配源。
- 重复恢复到已恢复库被拒绝，令牌版本保持 4；已有 marker 表的目标被拒绝，原 marker 值保持。
- 异常 jobs 外键样本先通过完整只读预检，在较晚表加载时由 PostgreSQL 拒绝；之后目标重新满足 pristine 检查，表、函数、扩展和序列均没有残留，再导入有效包成功。
- token_version 最大整数样本也先通过只读预检；全部表和序列设置后，安全版本递增发生真实溢出。整个事务回滚，新建序列连同结构一起消失，随后有效包重试成功。setval 对已存在序列一般不提供事务回滚保证；本原语只允许本事务新建的序列，不能扩展为现有库覆盖恢复。
- 提前取消不写入目标，目标仍 pristine。没有声称已测试进程硬杀、磁盘故障、回滚失败或 COMMIT 断线歧义；这些故障注入仍是后续门禁。

首轮真实集成在 SHOW server_version_num 的文本扫描到 Go int 时失败，没有进入建表/数据加载；改为显式整数查询后全部通过。失败证据保存在 `.toolchain/backup-restore-db-92f0304dec76`，该轮两个容器也已清理，不算作成功。

脚本可用 --fixture 指定仅含合成数据的归档、--binary 指定以 integration tag 构建的 Linux 测试二进制；默认复用此前合成归档及 `.toolchain/backup-restore.test`。所有失败样本仅由合成归档复制后在临时源库中修改，原归档保持不变。

## 回归

- Windows 后端全包：105 个顶层、276 个含子测试通过事件，0 失败，4 个既有辅助/未配置数据库测试跳过。日志 `.toolchain/backup-restore-windows-full-final.jsonl`。目标包 vet 通过。
- 首次 Windows 全包运行被沙箱阻止连接单测自己启动的本机 HTTP/Redis 假服务，13 个失败事件（含子测试）保存在 `.toolchain/backup-restore-windows-full.jsonl`；获准运行本地回环测试后全包重跑通过，没有降低断言或跳过这些测试。
- Linux backup/migration/storage 三包 race：48 个顶层、139 个含子测试通过事件，0 失败/跳过/竞争报告。日志 `.toolchain/backup-restore-linux-race.jsonl`。非 root、无网络、源码/docs/模块只读，未连接数据库。
- 选定表单测确认仅原始行进入 COPY 通道，拒绝跨表、序列 SQL、额外 SQL、结束标记缺失和非法选择。原先完整数据段检查保持有效。
- 65 项契约模型检查、16 份冻结摘要匹配；OpenAPI、DDL、历史迁移及备份冻结契约未改。

## 下一步

把数据库原语接入整体恢复操作：持有控制锁和验证后的目录句柄，重新核对计划及目标身份，恢复对象并检查实体引用/密钥，再执行恢复后应用读取验收和中断状态收敛。数据库和对象不能以两个局部成功直接宣告整体完成。生产目标、S3、Windows 安全文件驱动与完整业务恢复入口仍未交付；本轮未部署。
