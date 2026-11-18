# LocalFS 交接审阅报告内部协议 v1

日期：2026-11-16。先行实现边界：新增独立审阅报告，不修改已有冻结 manifest/config/plan/operation、43 个 API、DDL 或状态语义。此协议确定报告的结构、身份绑定、时间及保留规则；真实目标探测器、行为验证、报告与操作状态的 CAS 登记仍须后续实现验收。

## 目标配置核验边界

报告绑定三份现有控制记录的原字节 SHA-256、operation generation、plan/package/manifest、target instance/storage 和已发布对象树身份。身份核验输入还包含三个独立的非敏感摘要：database_identity_sha256（集群系统标识/数据库 OID/角色 OID 等规范元数据）、runtime_config_revision_sha256（操作员分配的非秘密配置修订标识及 keyring/JWT 配置引用）、redis_identity_sha256（实际 Redis 进程身份与逻辑数据库）。不能以路径、URL 字符串或同机推断物理隔离。

三个摘要只是后续实时探测器输出的绑定槽位。不得对密码、JWT 密钥、低熵秘密、密钥内容或私密路径直接做哈希写入报告；配置修订必须在配置内容变动时变化，但本轮模型无法保证操作员遵守。Redis run_id/OID 也不能单独证明持久身份或隔离，后续探测器必须完成双向隔离验证、角色/权限及同目标回读。禁止 FLUSH、自动修改用户密码/创建管理员、自动扩大权限或读取环境中的业务配置。

## 报告字段与检查集合

固定 report_format=ylreader-handover-review、report_version=1、scope=isolated-localfs-handover-review；包含 operation_id、backup_id、plan_digest、package_sha256、manifest_sha256、target_instance_id、target_storage_fingerprint、publication_tree_fingerprint、observed_generation、operation_record_sha256、attempt_record_sha256、publication_record_sha256、target_identity（三个摘要）、started_at、finished_at、expires_at、checks、deployment_authorized=false、ready_for_cutover=false。所有字段必填，不接受 null、重复/未知字段、非规范 UTC、额外 JSON 或超过 32KiB 的输入。

checks 固定顺序且恰好十项：schema_ledger、database_identity、database_role、published_objects、header_keyring、jwt_rotation、redis_isolation、sessions_revoked、tasks_disabled、runtime_behavior。每项仅 id/status，status 为 pass/fail/unavailable；不允许任意错误字符串、正文或凭据。失败/不可用报告也允许保留供审阅，但不能通过 validateFor 的全通过门禁。

started_at ≤ finished_at < expires_at；检查窗口和报告有效期各最多 5 分钟。所有时间精确到微秒、UTC。validateFor 还要求 started_at 不早于计划创建及当前操作更新时间，expires_at 不晚于计划的现有到期时间，finished_at ≤ now < expires_at。过期计划不能通过新报告续期；若耗时不适合现有 15 分钟计划，必须另行修订契约，不能悄悄忽略过期。

validateFor 必须重验 Plan 摘要、三份严格解析记录、数据库 committed、对象 verified、操作 validating 且未取消；核对全部记录互绑与报告字段，以及调用方提供的当前目标身份。仅检查 JSON 自洽绝不证明这些检查真实发生；三份控制记录与实时目标均需由受控守卫链重新读取。本轮不实现执行放行或导入外部 JSON 的放行路径。

## 持久化和确认丢失

通过现有安全 ConditionalFiles 能力排他写 operations/<operation UUID>/handover-review.json，不可覆盖。同一 operation 本轮只保留一份报告；报告过期/内容冲突时不自动重建、删除或续期，后续重验证的版本化策略另行设计。

CreateOnly 成功后严格回读并比较原始报告字节摘要。明确文件已存在返回冲突；其他写入错误、回读失败或摘要不一致统一返回 report unconfirmed，保留全部产物，不判断未写入、不自动重试或清理。即使后续只读看到同一报告，也不能把此前 durability-unknown 改称持久确认。报告与 state.json 的跨文件原子登记未完成，故不推进 ready_for_cutover/completed。

## 本轮验收

验证字段严格解析、检查集合和状态、时间边界、记录/计划/目标漂移、过期、取消及结果 unknown、并发排他创建、写入确认丢失、回读破坏与脱敏。纯模型及内存存储不能代替实际目标身份探测、安全文件驱动持久性、真实归档恢复或生产交接验收。本模块不部署、不启动服务、不连接数据库/Redis、不修改恢复状态。
