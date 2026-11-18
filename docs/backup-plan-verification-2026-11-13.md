# 配置、位置身份和恢复计划实现验证（2026-11-13）

依据 ADR-019 冻结设计继续实现。新增代码在 backend/internal/backup 与 storage 的附加能力中，没有注册 API、启动恢复命令、执行业务迁移或部署。实验配置仅支持 LocalFS 与回环隔离库；没有打开生产写目标。

## 本批实现

- LoadConfig 按冻结结构精确加载 JSON，递归拒绝重复键、未知字段、错误大小写、缺字段、非法 null、非整数值和无效 UTF-8；输入上限 1 MiB。校验格式、实例、预算、绝对文件引用及词法命名空间边界，不接受内嵌凭据或环境替代目标。
- LoadConfigFile / ReadBackupSecret 通过私有目录和文件句柄读取配置或目标 DSN，检查 owner、mode、ACL、链接及大小；不沿普通 os.ReadFile 路径绕开安全驱动。结构加载不自动读取源 DSN、加密 identity 或离线证明文件。
- OpenDirectory / Location / Empty 为 Linux 句柄绑定能力。位置身份来自 fstat 与 statx：设备、inode、mount ID 及祖先链。目标为空只读最多一个目录项；不创建业务目录。
- OpenLocations 打开已准备的 source、target、backup、control、temporary 命名空间，拒绝相同/祖先位置。对于同设备的不同 mount ID 保守拒绝，避免绑定挂载隐藏实际重叠；这也可能拒绝合法但无法证明隔离的挂载组合。不存在的目标需要先由操作显式预留，不在 plan 中静默创建。
- PreparePlan 绑定实例、包/manifest 摘要、schema/迁移摘要、目标数据库、三个存储指纹、预算及 15 分钟有效期；CheckPlan 重新读取安全 DSN、核对目标/目录身份、确认实例、包绑定和空状态。缺少数据库检查器直接拒绝。
- PostgresTargetInspector 只对经过门禁的回环隔离库连接，以只读事务确认非高权限角色拥有数据库、没有应用对象/额外扩展或模式，且无其他连接。不执行 DDL、授权、清理或迁移。连接/查询错误脱敏为稳定错误。

## 跨语言摘要修正

Go 计划摘要与冻结 Python 样例一致。另增加 independently computed 样例：created_at/expires_at 带 `.000000Z` 时摘要为 `e66974820c996447b9c83ba1f8cb5a18ed398d8405b02c5d6c986b7d2ac56768`。

实现初稿使用 time.Time，重新序列化会删除尾随小数零，导致合法的原始 JSON 计划摘要不匹配。已改为保留时间戳原始字符串，在校验/时序比较时解析；不修改冻结协议、样例或放宽摘要检查。

位置指纹固定使用前缀 `ylreader-localfs-location-v1` 加 NUL，随后为 big-endian uint64 的 device、inode、mount ID、namespace 的 UTF-8 字节长度，再接 namespace 字节；最终 SHA-256。指纹不包含密码、DSN 或私钥。

## 验证结果

- Windows 后端普通全包回归：90 个顶层通过、178 个含子测试通过事件，0 失败；4 个既有测试跳过（XPath 辅助 worker 与三个未配置数据库的服务测试），未计通过。日志 .toolchain/backup-plan-windows-unit-final.jsonl。
- Windows 目标包 vet 通过。
- Linux 两个目标包 race：29 个顶层通过、37 个含子测试通过事件，0 失败/跳过/数据竞争报告。日志 .toolchain/backup-plan-linux-race-final.jsonl。包括第一批基础测试及本批配置、摘要、目录身份替换、链接配置、DSN 权限、目标漂移与检查器失败样本。
- 真实 PostgreSQL 检查：脚本 scripts/verify-backup-plan-db.py 创建本轮唯一标签的独占临时集群，数据目录 tmpfs、无宿主挂载/端口、network=none。测试容器仅共享这个隔离集群的网络命名空间，通过回环连接；没有其他外网或业务库连接。
- PostgreSQL 实测通过：允许新空目标；拒绝额外连接、已有表、函数、类型、额外模式及高权限/非所有者角色；清理合成对象后再次检查通过，证明检查器未留下持久业务对象。该专项 1 个顶层/5 个含子测试通过事件，0 失败/跳过，使用 -race。
- 真实 DB 运行 ID `b3b83a26cdcc`，报告 .toolchain/backup-plan-db-b3b83a26cdcc/result.json，两个容器 cleanup=true；使用本机已有 PostgreSQL 16.15 镜像，不把它认定为新的生产安全基线。
- 65 项契约模型检查及 16 份冻结摘要继续匹配。OpenAPI、DDL 和历史迁移不变。

## 使用边界

本批生成的是只读预检后的协议计划，不是恢复执行许可。PackageBinding 必须由后续完整认证/manifest 校验产生；填入摘要字符串本身不能认证包。配置引用的 identity、quiescence 内容、包对象和实际可用空间尚未在这层核验。

CheckPlan 会关闭预检句柄。未来 apply 必须在执行阶段重新校验并持有目标句柄、控制锁和数据库独占条件，且在写入检查点再核对时效；不得将本次预检成功缓存为之后任意路径写入的许可。数据库检查核对连接目标描述和当前所有者/结构，不提供管理员可替换的数据库集群的密码学身份保证。

当前安全驱动只接受已准备的当前 UID 私有目录；不会 chmod 现有应用卷来使其通过，也不会自动创建或清空数据库。Windows 原生安全驱动仍未实现；S3、生产入口、容量检查、封装与数据库/对象/密钥完整恢复仍待后续交付。

下一步接入包认证、manifest/归档条目与摘要校验，再把已验证包绑定交给计划层；之后才接入受限 apply 编排和端到端故障验收。
