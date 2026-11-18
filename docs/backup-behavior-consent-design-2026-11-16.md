# 通用行为验收授权意向与凭据输入契约

本协议先于实现，不改变冻结 OpenAPI、DDL、备份格式及现有 isolated-http-fixture-v1 报告桥接。此次仅实现内部只读输入预检，不提供 CLI/API、HTTP 客户端或执行授权。

## 当前实现边界

意向文件为严格 JSON（最大16KiB），format=ylreader-behavior-consent，version=1，scope=isolated-account-verification-only。必须显式提供 consent_id、operation_id、owner_id、plan_digest、target_instance_id、observed_generation、operation_record_sha256、attempt_record_sha256、publication_record_sha256、runtime_config_revision_sha256、issued_at、expires_at、accounts。每个 account 只含 user_id 与 credential_key。无默认用户、无环境变量、无 URL/外部 endpoint、无密码重置或管理员创建选项。

issued/expires 为规范 UTC 微秒精度；窗口最多5分钟，须落在计划有效期内。当前时刻必须已发行且严格早于过期。来源为操作员控制的既有私有输入目录；文件本身不是签名授权，不证明账号所有权、运行配置或数据库身份实测。

accounts 1至8个，UUID、相对 credential_key 均唯一，不能指向意向文件；禁止穿越/绝对路径。凭据文件严格 JSON（最大32KiB）：user_id、username、password、old_access_token、old_refresh_token。ID 须对应所列账号，username 1..256字节，password 1..4096字节，old_access_token 1..16384字节，old_refresh_token 为64位小写十六进制。密码不 trim，不规范化；此处只检查形状，不验证密码、JWT 或旧令牌时效/撤销状态。

输入和控制区只通过安全驱动 Get 能力读取；不允许 os.Open 降级。先核对已有稳定控制记录及 validating/committed/verified 状态，绑定计划、owner、generation、三记录摘要和显式运行配置修订摘要。运行配置修订摘要是可信调用者提供的预期值，不接受仅从意向文件自我证明。

凭据及意向二次读取按原始字节比较，控制记录前后稳定读取并重新核对，最后再次核对时间和取消。变化、缺失、不安全文件、超限、格式/关闭失败一律返回固定错误；失败不产生部分成功结果。不写数据、不联网、不创建文件、不修改控制记录。重复读不能提供原子快照或防 ABA 保证。

准备结果为内部不透明输入，禁止 JSON 序列化，常见 fmt 格式输出统一脱敏；不含 behaviorGrant，不被现有合成报告桥接接受。Close 丢弃凭据引用；尽可能清除读取缓冲，不承诺 Go 字符串/GC/库内副本安全擦除。凭据不进入日志、摘要、快照、审计包或报告。

## 后续执行器必须遵循，尚未实现

真实用户行为预检须在 live restoreGuard 下核对源/目标 JWT、数据库账号/密码及旧令牌有效性，绑定真实目标数据库、对象、Redis 与同次运行配置，不接受外部 JSON 的 pass 声明。没有有效旧令牌时，旧 JWT/refresh 撤销项为 unavailable，不能以过期充当撤销成功。

动作只允许已同意用户登录/登出、旧凭据拒绝探测和读取已恢复数据；不自动写阅读进度/书签，不抓取外站，不更改密码/角色。现有夹具含写操作，不能直接作为真实用户运行器复用。

首次登录前必须在控制区以 create-only 持久登记 consent_id、绑定及动作意向；登录可能已创建会话但响应丢失，因此派发后取消/超时/崩溃一律视为结果未知，不自动重试登录、不自动扩大授权、不得仅凭本机对象重建许可。进程内互斥不是持久防重放。此动作账本及通用运行器将在后续实现，当前预检可重复且不能执行动作。

已知会话 UUID 只允许受限记账及显式登出；确认丢失保留证据，不 FLUSH、不清空用户会话表。中断使用独立有界上下文做只读诊断；恢复数据和对象保留，不把数据库已提交后的失败描述为完整回滚。成功仍不赋予 ready_for_cutover、completed 或部署权限。真实用户报告 scope、登记协议及操作员交接须另行明确，不偷偷放宽现有 isolated profile。
