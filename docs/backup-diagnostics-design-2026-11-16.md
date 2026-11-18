# 恢复中断只读诊断内部协议 v1

日期：2026-11-16。范围：现有冻结 LocalFS 隔离恢复协议的诊断辅助模块；先定义下列边界，再实现和测试。不修改备份格式、公开 API、DDL 或恢复状态机。本模块不是正式交接报告或执行凭证。

## 输入与输出

输入仅为已经安全打开的控制区只读 Get 能力与规范 operation UUID，不读取环境配置、DSN、密钥或真实数据库。复用现有严格解析器读取 state.json、attempt.json、publication.json。拒绝非法 UUID，禁止通过输入构造任意文件路径。

输出使用固定结构：diagnostic_format=ylreader-restore-record-diagnostic、diagnostic_version=1、scope=control-records-only、operation_id、status、reason。deployment_authorized 与 ready_for_cutover 始终为 false。仅当全部记录解析、交叉绑定及第二轮原字节摘要比对成功，才附带 observed_generation、database_outcome、publication_state。没有自由文本错误、宿主路径、DSN、正文、用户信息、密钥、对象 key 或暂存引用。

status 为 unavailable / inconsistent / changed / blocked / consistent_records。reason 使用固定代码：invalid_input、record_missing、record_invalid_or_unavailable、binding_mismatch、snapshot_changed、database_unconfirmed、publication_unconfirmed、operation_not_validating、cancellation_requested、records_match。读取失败不输出底层错误。

## 交叉绑定与一致性

三份记录 operation/backup/plan/owner 必须相同；attempt 与 publication 的目标存储指纹、暂存 generation 必须相同。数据库结果必须明确 committed，对象发布必须 verified，操作必须 validating 且无取消请求，才能得到 consistent_records。unknown、rolled_back、not_started 都不能从对象存在或行数匹配推断 committed。

完整读取两轮并比较每份原字节 SHA-256；任何第二轮缺失/损坏/变化输出 changed，不输出已验证字段。该措施检测观察期间的部分变化，**不是原子快照、租约或 ABA 防护**，不能防止最后一次读取之后的变化。调用方不能把本结果转换为 CAS 令牌或执行资格。

## 能力和后续门禁

诊断只接受 Get 接口；现有记录解析器所需写方法由本地适配器直接拒绝，绝不转发。无数据库连接、锁、重试执行、清理、状态持久化、服务启动或所有权接管。缺失记录不自动创建。

正式报告仍需实际目标身份、配置/Redis/JWT/keyring、对象与数据库实时核验、受控行为验证及持久 CAS 登记，另行确定契约。本模块不推进 ready_for_cutover/completed，也不证明 plan 当前有效、恢复已成功或正式环境可以切换。后续只读 CLI 可使用本输出，执行命令必须走独立守卫链。

## 本轮验收要求

测试缺失、畸形/重复字段、跨操作或目标绑定漂移、数据库 unknown/rolled_back、发布未确认、取消、终态、观察中变更、非法路径、上下文取消与底层敏感错误脱敏。只读测试存储不提供写能力；验证调用前后原字节不变。纯模型测试不能替代真实目标恢复或故障验收。
