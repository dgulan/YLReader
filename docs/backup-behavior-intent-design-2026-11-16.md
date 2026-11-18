# 登录前持久动作意向与结果未知诊断契约

先行内部协议，不修改冻结 API、DDL、备份归档或既有报告 profile。

## 登记边界

控制区路径固定为 `operations/<operation UUID>/behavior-intent.json`。每个恢复操作只保留一个槽，不能通过更换 consent_id 再次登记；暂不提供覆盖、删除、重置或重试接口。记录使用安全 ConditionalFiles.CreateOnly，成功后严格回读并重验控制记录与时效。写入返回未知、回读失败、绑定变化或取消均保留记录，返回固定“登记未确认”，不能从磁盘回读重建历史成功确认。

记录最大16KiB，严格 JSON：intent_format=ylreader-behavior-intent、intent_version=1、scope=isolated-login-intent-only、state=dispatch_reserved；另有 consent_id、operation_id、owner_id、plan_digest、target_instance_id、observed_generation、operation_record_sha256、attempt_record_sha256、publication_record_sha256、runtime_config_revision_sha256、user_ids、issued_at、expires_at、recorded_at。绑定来自先前已准备的输入，只记录账号 UUID，不含 username、口令、令牌、文件路径、秘密摘要或正文。时间窗口仍最多5分钟，recorded_at 必须在有效期内。

内部登记核心先检查预检输入/账号形状，再核对稳定的 validating/committed/verified 控制记录与预期运行修订，执行唯一 create-only，回读比对完整字节摘要及绑定，最后再检查同版本记录、时间和取消。登记不改变操作 generation/phase、报告、对象或数据库；无网络请求。

此登记只是持久阻止同操作重复登记的基础，不证明真实账号授权、live guard 排他或网络调用发生。暂不提供派发许可、登录回调或可由 JSON 恢复的凭证。后续执行器必须在 live guard 下完成真实账号/令牌/目标验证后才调用登记，再紧邻派发重验排他与取消；仍需一次性派发能力和有界会话结果记账，不能仅因记录存在就调用登录。当前预检及登记核心均是内部模块，没有 CLI/API 运行入口。

## 只读诊断

诊断使用只有 Get 的接口；两轮读取操作/attempt/publication与可选意向，存在性或摘要变化返回 changed。只有 ErrNotFound 算缺失，权限、格式、关闭错误不能解释为不存在。检查操作/owner/计划/目标存储/package/manifest/staging 的关联，以及意向三记录摘要、版本、计划、目标的关联；不重新认证目标或凭据。

输出只含固定格式/scope、operation_id、状态/原因、历史持久确认=not_inferred、dispatch_authorized=false、retry_authorized=false、deployment_authorized=false。状态：intent_not_observed、outcome_unknown、stale、inconsistent、changed、unavailable。

诊断不加载原计划或运行配置；记录内 target_instance_id/runtime 修订仅为登记时绑定声明，不能在此次诊断中重新证明目标身份。完整计划/目标/运行修订匹配由登记前校验承担，后续 live guard 派发前必须再次核对。

即使 dispatch_reserved 可见且一致，登录也可能尚未发送、已发送无响应、或已成功而进程崩溃，因此统一 outcome_unknown；过期/后续版本变化归 stale，仍不代表动作未发生。没有意向也不证明其他实例/历史上没有登录，只说明本轮未观察到意向。诊断永不派发、补写、重试或清理。

并发测试要求同操作仅一个 CreateOnly 胜者；确认丢失后再次登记必须被阻止。独立重开驱动验证持久存在和控制记录不变。故障测试覆盖写前/写后取消、写确认丢失、回读失败、记录变化、过期、冲突、畸形/不安全文件和读中变化。普通重复读不提供原子快照或 ABA 保证；若存储卷被外部删除/替换，本模块不能跨卷实现全局防重放，后续 live guard 必须验证物理身份。
