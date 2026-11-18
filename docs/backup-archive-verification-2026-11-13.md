# PostgreSQL 归档目录检查验证（2026-11-13）

本轮继续 ADR-019 的隔离恢复实现，交付只读目录检查。没有执行归档 SQL、连接业务数据库、创建恢复目标、注册 API 或部署。

## 已实现

- `backend/internal/backup/archive_tool.go` 只调用操作用户显式指定的受信绝对路径 `pg_restore`，不通过 shell 或 PATH 查找。工具版本要求 PostgreSQL 16；调用参数只包括 `--version` 和 `--list`，归档由已认证包的数据库 reader 经 stdin 提供，没有数据库目标参数。
- 子进程只保留 C locale 和 Windows 必需的 SystemRoot，不继承 `PG*`、数据库 URL、密码或服务配置环境。输出最多 256 KiB，单行最多 4096 字节，子进程最长 60 秒，进程退出后的管道等待上限 1 秒。工具错误统一为稳定错误，不回传可能包含源库名称的 stderr。
- `archive_inventory_v1.txt` 来自本项目不可变迁移 v1 在隔离 PostgreSQL 16 合成库上的真实目录，共 136 项。匹配目录类型、schema 和完整对象签名；TOC ID/OID 只允许 uint32 十进制，拒绝重复 ID、重复签名、缺项及所有额外条目，包括未知表、函数、序列、BLOB 和外部 schema。目录检查绑定迁移版本及基线 SHA-256，后续迁移不能静默复用此目录。
- 此窄范围实验只接受普通 ASCII PostgreSQL owner 标识，不尝试解析任意带引号角色名。owner 不进入输出选择清单；其他命名或版本需单独评审兼容性。
- 从通过的目录重建 14 个 TABLE DATA 和 2 个 SEQUENCE SET 的选择清单，按冻结设计中的外键依赖顺序排列。不会转发归档提供的原始自由文本，也不选中任何 DDL、权限、函数或触发器。
- `VerifiedPackage.InspectDatabaseInventory` 在外部工具运行期间保留私有暂存读取句柄，避免包被并发清理。检查结果不构成 apply 授权。

目录是名称白名单，不是 SQL 沙箱。外部工具处理归档时仍可能分配自己的内存；输出限额和超时不构成子进程 RSS/CPU 配额。包必须来自受信源，后续恢复仍限定独立隔离目标，且只能使用可信基线提供 DDL。

## 验证

- Windows 两个目标包：25 个顶层、81 个含子测试通过事件，0 失败/跳过；日志 `.toolchain/backup-inventory-windows.jsonl`。目标包 vet 通过。
- Linux 两个目标包真实 race：42 个顶层、98 个含子测试通过事件，0 失败/跳过/竞争报告；日志 `.toolchain/backup-inventory-linux-race.jsonl`。非 root、无网络、源码/docs/模块只读。
- 官方 PostgreSQL 16.15 工具读取既有真实合成 `database.dump`，接受 136 项目录并产生 16 项数据选择清单；拒绝错误魔数及截断至仅魔数的输入，取消上下文被传播。测试同时设置 PGHOST/PGDATABASE/PGOPTIONS 为不可连接标记，检查子进程没有依赖这些值。独立 integration 测试 1 项通过，日志 `.toolchain/backup-inventory-real-pg.txt`。
- 外部工具测试使用已有 `postgres:16` 镜像、无网络、只读文件系统、非 root 用户，仅挂载测试程序和合成归档为只读；没有启动 PostgreSQL 数据库进程。合成归档来源为 `.toolchain/backup-db-01437dc0b289`，来源及最初 20 项数据库实验见 `backup-db-prototype-2026-11-13.md`。
- 65 项契约检查通过、16 份冻结摘要匹配。OpenAPI、DDL、历史迁移及备份冻结文档均未修改。

首次编译发现测试已有同名 archiveEntry 类型，已将新正则变量更名；首次容器测试因 PowerShell 对未完整引用的 test flag 拆分失败，改为完整引用的 `-test.run=...` 后重跑通过。两次失败没有算作测试通过或绕过检查。

## 后续门禁

`pg_restore --list` 只读取目录，不证明压缩数据段完整、COPY 列形状匹配、实际表行数匹配或账本内容有效；也不能保证发现仅数据区发生的截断。外层包摘要通过不等于这些数据库语义通过。下一步应在不连接数据库的模式下有界展开数据，检查可信表/列列表、14 表计数、迁移账本和 2 个序列值，再接入新隔离目标的单事务数据恢复编排。

暂未交付恢复 apply、真实 header 密钥解密、对象引用一致性和完整恢复闭环。生产目标、S3、Windows 安全文件驱动及发布仍不在本轮交付范围。

当前分支仍为 `codex/pre-release-20261113`；重新查询 PR #1 为 open/draft、未合并，head 仍为 fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。本轮改动留在工作区，未提交、推送、合并或部署。
