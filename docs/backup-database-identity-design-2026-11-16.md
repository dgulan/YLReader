# 交接数据库只读身份探测内部协议 v1

日期：2026-11-16。先于实现定义；补充交接审阅报告的 database_identity 输入，不改变既有冻结协议、DDL/API 或恢复状态机。本轮只实现数据库探测，运行配置、Redis、实际报告编排与操作状态 CAS 仍待后续。

## 信任边界和身份

入口仅接受显式隔离 DSN，复用 ParseTarget 的回环地址、yreader_a1_iso_* 名称及 PG* 环境拒绝规则。调用方必须独立提供源和目标的预登记 pin：cluster system_identifier（规范非零十进制 uint64）、database OID 和 role OID（非零 uint32）。不能每次通过当前连接重新生成期望 pin 来消除漂移。

若源/目标 cluster + database OID 相同，即使角色不同也拒绝；不同数据库可以在同一隔离集群。pin 必须由可信配置/预配置流程获得，当前模块不声称能证明 pin 的来源。PostgreSQL 物理克隆、恢复后 OID 重用、恶意管理员和连接后的非协作变化仍需外部隔离与恢复守卫解决。

实际连接在最多五秒上下文内建立 RepeatableRead + ReadOnly 事务，只执行固定 SELECT 和事务控制；PG16、当前数据库/会话用户匹配显式 DSN、数据库由该角色拥有、角色可登录、无 superuser/createdb/createrole/replication/bypassrls、无直接角色成员关系、无其他同库会话，事务确实只读且隔离级别正确，缺一拒绝。成员关系检查采取保守策略，连监控角色成员也拒绝；不是任意 ACL/SECURITY DEFINER 函数审计。

通过 pg_catalog.pg_control_system()、pg_database 和 pg_roles 读取实际 system_identifier/database OID/role OID，必须与目标 pin 一致。系统函数执行权限不足时返回固定不可用错误，绝不 GRANT、SET ROLE 或降级到 DSN/路径摘要。隔离测试由自有 fixture 管理员预先给予 reader 对该单个系统函数的 EXECUTE；探测器本身不授予权限。

## 账本和输出

先核对 public.schema_migrations 为当前角色拥有的普通、非 RLS 表，再核对其恰有一条记录且 version/name/checksum 与 migration.Expected 一致；静态查询完全限定 pg_catalog/public，不受 search_path 影响。不执行迁移或修复账本。此检查验证账本，不等于完整 DDL/约束指纹审计，不能独自满足 schema_ledger 的所有最终交接要求。

全部检查和只读事务提交成功后，仅输出身份摘要：SHA-256(UTF-8 JSON，固定字段顺序 format=ylreader-database-identity、version=1、system_identifier、database_oid、role_oid)。不含 DSN、密码、路径或密钥；不输出原始 SQL 错误或连接诊断。任何失败不返回部分摘要。摘要可填入 handoverTargetIdentity.Database，但不会自动填 pass、写报告或推进状态。

## 验收

单测检查 pin 规范化、源目标别名、指纹敏感性与非法目标提前拒绝。使用自有无外网、无宿主端口、tmpfs 的 PostgreSQL16 容器实际验证成功、权限不足、身份漂移、同库别名、其他会话、账本破坏、危险角色/成员关系和取消；比较探测前后业务行、账本与权限，确保探测没有修复或写入。所有 fixture 写操作仅由显式隔离测试设置/破坏数据，不由探测器执行。

参考 PostgreSQL16 官方 [系统信息函数](https://www.postgresql.org/docs/16/functions-info.html) 与 [预定义角色](https://www.postgresql.org/docs/16/predefined-roles.html)。本模块不部署、不启动业务服务、不连接业务数据库。
