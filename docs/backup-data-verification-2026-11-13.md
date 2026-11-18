# PostgreSQL 数据段只读校验（2026-11-13）

继归档目录检查之后，本轮实现 `archive_data.go`、固定 COPY 列清单及其测试，并在 VerifiedPackage 上增加 VerifyDatabaseData。未注册业务接口、连接业务数据库、执行归档 SQL、提交、推送、合并或部署。

## 验证链及边界

VerifyDatabaseData 只接受已经完成 age、manifest 和 tar 认证的私有包，先重新打开数据库归档检查目录，再重新打开它进行数据段展开。整个子进程操作持有读取句柄，清理不能删除正在使用的私有暂存数据；两次操作之间如包已关闭，则后一次打开失败，不继续验证。

展开使用受信绝对路径的 PostgreSQL 16 pg_restore，固定参数为 `--data-only --no-owner --no-acl --file=-`，归档通过 stdin 输入，没有数据库参数。官方工具在不指定数据库时输出脚本；data-only 包括表数据和序列值，见 [pg_restore 文档](https://www.postgresql.org/docs/16/app-pgrestore.html)。本实现只把 stdout 作为待检查的文本流，绝不执行、缓存为可执行脚本或转交 psql。

- 14 张表的 COPY 列名及顺序精确匹配当前不可变迁移。静态列清单绑定现有迁移摘要；回归测试从仓库内的可信基线直接提取并逐表比较，避免测试仅从同一份列清单构造样本而漏掉清单漂移。该测试使用的简化解析只用于本项目固定 SQL 格式，不用于不可信归档 DDL。
- 校验固定 prologue；表区与 COPY 行区使用独立状态。每行检查 UTF-8、无 NUL、字段数；COPY 结束时核对每表实际行数，额外行立即拒绝。COPY 的 text 格式默认用 tab 分隔、反斜杠表示转义，见 [COPY 文档](https://www.postgresql.org/docs/16/sql-copy.html)。正文中的 SQL 样式文本留在数据字段中，不会作为语句解析或执行。
- schema_migrations 必须恰好一行，version/name/checksum 精确匹配迁移基线，applied_at 不能是空值或 NULL。这里不解析 applied_at 的完整 PostgreSQL 时间语义，也不代替数据库类型和约束检查。
- 只接受 chapters_id_seq 和 source_failures_id_seq 的固定 setval 语法，分别恰好一次；检查 int64 范围、正值及 called 状态。chapters/source_failures 的 id 必须为正 int64；序列下一值必须高于已见最大 ID，拒绝回退碰撞和已耗尽的已调用最大值。
- 拒绝未知 SQL、INSERT、COPY PROGRAM、未知/重复表、列漂移、未知/重复序列和其他 psql 元命令。较新 PostgreSQL 16 工具生成的 restrict/unrestrict 标记必须匹配、位置正确；兼容不带该标记的旧格式，带标记时不能丢失结束标记。
- 调用方显式传入独立的展开文本预算，范围 1 至 1 TiB；这不是压缩归档或 tar 的字节预算，也不是性能承诺。单行扫描缓冲上限 8 MiB；官方工具进程沿用 60 秒超时、受限环境及诊断脱敏。解析错误会取消并回收子进程；有效文本仍须等待官方工具成功退出，不能忽略解压错误。

不保留完整数据文本或行索引；仅保存 14 张表的计数、两个最大 ID 和序列状态。仍可能有单行缓冲及工具自身内存分配，不能将本层预算称为系统级 RSS/CPU 配额。超大合法字段或其他输出格式变化可能被此窄范围实验拒绝。

成功表示固定列形状、行数、账本标识和序列关系通过。尚不验证所有字段类型、COPY 转义的完整语义、UUID 唯一性、外键、业务引用或所有 PostgreSQL 约束；也不构成任意归档 SQL 的安全证明或恢复写入授权。受信来源与独立隔离目标的限制继续成立。

## 测试结果

- Windows 目标包：27 个顶层、114 个含子测试通过事件，0 失败/跳过；日志 `.toolchain/backup-data-windows.jsonl`。目标包 vet 通过。
- Linux 目标包 race：44 个顶层、131 个含子测试通过事件，0 失败/跳过/竞争报告；日志 `.toolchain/backup-data-linux-race.jsonl`。非 root、无网络，源码/docs/模块只读。
- 官方 PostgreSQL 16.15 工具验证使用既有合成归档 `.toolchain/backup-db-01437dc0b289/database.dump`，实际计数取自此前独立保存的 source_snapshot；14 表共 21 行，chapters 序列为 2/called，source_failures 为 1/not-called。
- 新增两个真实工具 integration 测试：直接展开并核对合成库数据，以及将真实合成 dump 封装进 age/tar 后，使用 Linux 安全 LocalFS 暂存完成认证、目录和数据检查，关闭后核对暂存区为空。后者的对象仍是合成对象，不代表应用恢复或 header 解密通过。
- 真实工具测试拒绝行数不匹配、展开预算过小和截断至一半的归档。与既有目录 integration 测试及本轮纯模型测试一起运行，日志 `.toolchain/backup-data-real-pg.txt` 共 5 个顶层、38 个含子测试通过事件，0 失败/跳过。此组没有启用 race；race 结果来自前述目标包单独回归。
- 单测覆盖列改名/换序、漏/重复表、账本字段错误、COPY 未结束、额外 SQL/元命令、非法 UTF-8/NUL、行/总字节预算、读取失败、取消、序列溢出/耗尽/重复/缺失和下一值碰撞。
- 65 项冻结契约检查及 16 份冻结摘要匹配；OpenAPI、DDL、历史迁移和备份冻结文档没有修改。新增文本列清单使用 LF Git 属性，避免 Windows 检出改变匹配结果。

## 下一步

在新隔离目标中实现可信基线建库和单事务数据加载，失败整体回滚；不得直接执行本轮展开的脚本。还需把对象/实体引用核对、会话撤销、任务终止、header 密钥解密及恢复后应用读取纳入整体编排。完整备份恢复闭环尚未交付，生产与发布入口继续关闭。
