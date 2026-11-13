# 版本化迁移设计（ADR-017，Frozen v1.5 修订 1）

2026-11-13：本修订先冻结设计，再实现。API 操作及响应不变；readyz 的 schema 检查升级为版本、校验和及视图形状检查。旧版数据导入、chapter_cache 物理删除不在范围。

schema_migrations(version bigint 主键, name text, checksum char(64), applied_at timestamptz) 为迁移账本。第一版为当前权威 schema 的基线；SHA-256 基于 UTF-8/LF、去除最外层事务及账本登记语句后的 SQL。docs/schema.sql 与内嵌基线必须由测试核对，避免双重权威。新库 schema 初始化在同一事务登记基线，旧库通过显式 up 补齐幂等结构并登记。

迁移工具提供 schema status/check/up。status/check 只读；up 使用单事务、事务级 advisory lock，锁后重新读取账本，先拒绝未知版本或校验和漂移，再执行未应用 SQL。DDL 与账本同事务，失败整体回滚；不提供自动 down，恢复依赖预先备份及人工审查。启动和健康检查绝不执行 DDL。

写入工具仅允许显式回环地址、端口及 yreader_test / yreader_a1_iso_* 数据库，拒绝目标相关 PG 环境覆盖，生产迁移入口另行评审。Makefile 原 Python 门禁保留；CLI 再验证 pgx 解析后的实际目标，不打印 DSN。CLI 不提供绕过选项。

验收覆盖空库、已有数据升级、重复执行、并发迁移、失败回滚、校验和漂移和未知新版本。迁移测试使用独立 schema/隔离集群，不能清空现有数据库。CI 运行相同测试，不将本机结果称作远端已运行。
