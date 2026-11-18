# 备份基础能力第一批实现验证（2026-11-13）

依据 ADR-019 / LocalFS 隔离实验设计 v1。实现范围为受限目标解析、操作状态控制以及 Linux 句柄绑定文件能力；没有新增 API 路由、可执行备份恢复命令或数据库连接。现有应用仍使用原 Provider，不自动接入新控制区。

## 已实现

### 受限数据库目标

backend/internal/backup/target.go 仅接受显式 PostgreSQL URI：用户名、密码字段（隔离信任环境可显式为空）、回环 host、有效端口、yreader_a1_iso_* 新隔离库名称。生产库和既有通用 yreader_test 均拒绝，不支持 keyword/value 连接串或 service 文件。所有 PG* 环境变量均导致拒绝；URI 只允许单值 sslmode/connect_timeout/application_name，拒绝 host/options/passfile 等覆盖项。localhost 在返回配置中固定为回环 IP，不延后依赖 DNS。

返回错误不包含 URI、密码或底层解析错误。解析不连接数据库，也不证明数据库新建/为空；连接后归属、结构、空目标和独占权限检查留给恢复编排，不能仅凭这个函数签发写入许可。

### Linux 文件能力

backend/internal/pkg/storage/backup_files* 是独立附加能力，不增加既有 Provider 的必需方法。OpenBackupFiles 拒绝 root 进程，只打开已存在、当前 UID 所有、0700 且无额外 POSIX ACL 的根目录，逐级 openat/O_NOFOLLOW；后续操作绑定目录句柄。常规文件要求 0600、当前 UID、nlink=1、无额外 ACL，拒绝符号链接形式的目录别名和不支持安全属性的后端；挂载别名之间的整体重叠检查仍属于后续计划校验。

CreateOnly 流式写同目录私有临时文件，校验声明长度、fsync 文件，以 renameat2/RENAME_NOREPLACE 排他发布并 fsync 目录；不支持该原语时拒绝降级。CAS 使用稳定的私有锁文件和非阻塞 flock，在锁内验证原文件完整 SHA-256，原子替换并同步。锁文件不按年龄删除；进程退出由 OS 释放锁。

目录同步失败返回 ErrCommitUnknown：重命名可能已生效，调用方必须刷新状态，不能认为“错误就等于未写入”。NewTemp 在已打开的独占根内创建新的私有目录，只允许平面文件；Remove 先检查目录身份及所有子项，拒绝链接，清理只针对本轮句柄和名称，不对配置路径做递归删除。

非 Linux 平台 OpenBackupFiles 明确返回 ErrCapability。之前的 Windows ACL 原型不等于已经实现和验证 Go Windows 文件驱动，本批不提供普通 LocalFS 回退。

### 操作控制

backend/internal/backup/control.go 定义冻结 operation 结构，使用 ConditionalFiles 持久保存 operations/<operation_id>/state.json。Create 只允许初始 planned/generation=1；Update 先验证绑定、generation、时间和阶段转换，再以读取内容摘要 CAS。Load 限制 1 MiB，拒绝重复/未知/缺失字段、非法 null、非 UTC-Z 时间以及尾随数据。

终态不可改写；取消标记不能复位，取消后不继续推进；stale 仅用于状态提示。operation_interrupted 需要调用方明确提供进程死亡确认，本批没有实现自动回收或“子进程已退出”的证明，不能按心跳超时强制恢复执行。

## 验证

- Windows 普通后端全包回归：85 个顶层通过、173 个含子测试通过事件、0 失败；4 个测试跳过，分别为 XPath 子进程辅助入口及三个未配置数据库的服务测试，不计为通过。原始日志 .toolchain/backup-foundation-windows-unit-final.jsonl。
- Windows 两个目标包 vet 通过；非 Linux 驱动拒绝降级的测试通过。
- Linux 两个目标包 race：19 个顶层通过、27 个含子测试通过事件、0 失败/跳过/竞争报告。使用已有 Go 1.26.11/race 镜像，非 root UID10001、无网络、源码/docs 只读、临时编译目录与独立缓存；未启动数据库。日志 .toolchain/backup-foundation-linux-race-final.jsonl。
- Linux 覆盖两个独立句柄下的真实控制记录并发更新、并发 CreateOnly 仅一个成功、旧摘要不能覆盖、临时写失败不发布/无 pending 残留、取消前拒绝、硬链接/符号链接/额外 POSIX ACL/宽松权限拒绝、根路径被替换后仍绑定原目录、清理不跟随链接、进程被终止后锁自动释放、fsync 失败结果不确定及重新读取后的旧 token 拒绝。
- 65 项契约检查及 16 份冻结摘要匹配。现有 OpenAPI、DDL、历史迁移字节和业务行为未改变。

首次 Linux 验证因 tmpfs 默认 noexec 无法执行测试二进制，没有运行用例；改为仅在隔离容器临时目录显式启用 exec 后重跑。首次普通回归的既有导入测试因系统临时目录不可写返回 500；改用本轮专属 TMP/TEMP 后通过，未修改旧测试或放宽断言。原始失败日志保留，不计为通过。

## 交付边界与下一步

尚未实现配置文件加载/真实路径组的身份与重叠检查、空间配额检查、稳定的计划指纹生成、生产目标、Windows 安全文件驱动、S3、封装工具入口以及数据库/对象/密钥的端到端恢复。当前目录句柄绑定和文件验证不能替代多命名空间的整体 plan/apply 校验。

本批 fsync 故障采用可控注入，不是断电实测；跨进程锁测试主动终止本轮创建的测试进程，不代表所有数据库子进程故障场景已覆盖。后续先实现严格配置加载、位置身份及计划生成/校验，再接入封装和受限恢复编排。

Go 模块只将既有 x/sys v0.50.0 标记为直接依赖，未升级或新增第三方版本。未提交、推送、合并、部署或连接业务数据库。
