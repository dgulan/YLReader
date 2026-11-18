# 交接报告采集编排内部协议 v1

日期：2026-11-16。先行定义本轮实现，不改变公开 API、DDL、冻结备份结构或操作状态。本轮连接现有数据库/运行配置/Redis 探测，生成并保存**部分已核验**的审阅报告，不宣称正式交接或状态登记完成。

## 采集顺序

显式传入有效 Plan、安全控制区能力、已预登记的三类探测配置与 UTC 微秒时钟。生产适配器直接调用 probeHandoverDatabase、probeHandoverRuntime、probeHandoverRedisPair；不接受外部 JSON 的检查结果。运行配置的 target_instance_id 必须与计划目标一致。内部可替换函数只用于确定性单测，不是公开插件或执行入口。

先严格读取 operation/attempt/publication 三份记录，验证与 Plan 的全部绑定、validating 且未取消、committed/verified。采集前检查计划有效期及操作更新时间，避免无效目标触发探测。记录开始时间，在最多三十秒上下文内依次采集三个身份摘要；任一步失败/取消/非法摘要均返回固定错误，不返回或保存部分身份。

采集后重读三份记录，其原字节摘要必须与采集前完全一致。完成时间、检查窗口与报告有效期遵守已有 review v1，expires_at 取 finished_at+5分钟与计划原到期时间的较早者，不能续期计划。逆行时钟、过期或读取中状态变化均拒绝。

## 检查语义

报告十项检查保持既有顺序。schema_ledger、database_identity、database_role、redis_isolation 在上述探测成功后标 pass；这里只核对基线账本及保守角色条件，不扩大为完整 DDL/ACL 审计。运行配置摘要绑定实际内存装配检查，但 header_keyring、jwt_rotation、published_objects、sessions_revoked、tasks_disabled、runtime_behavior 仍为 unavailable：没有把装配自检变成全量 header、旧令牌拒绝或运行行为证明。

将既有 validateFor 拆为共同身份/时效绑定检查与额外的“十项全通过”检查；原 validateFor 的全通过要求保持不变。采集器仅调用共同绑定检查，部分报告不能通过执行门禁。deployment_authorized、ready_for_cutover 仍固定 false。

## 保存与失败

保存前再次严格读取控制记录，并校验报告绑定、时效及两轮稳定摘要；使用现有 handoverReviewStore.Create 排他保存并回读。任何写入/回读确认未知均返回既有 unconfirmed，保留所有产物，不重试覆盖或删除。

保存后再读两轮控制记录并重验绑定和时效。记录已变化、取消或过期则返回固定阻断错误，报告可能已存在且保留，不能把它描述为回滚或放行。重复调用遇到已有报告返回冲突，不自动覆盖。

上述读检查不是原子快照、租约、CAS 或 ABA 防护，不能保证最后一次读取之后不再变化；不会对 state.json 做写入或所有权接管。本轮**不实现报告与 operation 的持久 CAS 登记**，后续必须在已有守卫和排他能力内另行设计验证，不能把当前报告保存说成状态登记完成。

## 验收边界

单测探测失败/畸形摘要/取消、采集前拒绝、记录变化、时间边界、部分报告不得通过门禁、保存确认丢失及保存后状态变化。真实隔离 PostgreSQL16 + 两个独立 Redis7 运行生产探测适配器，通过安全文件驱动在本轮私有 tmpfs 控制区保存部分报告，关闭重开后回读，验证控制记录不变。该夹具未执行真实归档恢复或对象/header/HTTP 验证；tmpfs 排他保存和重开回读不能伪装成断电/崩溃持久性或 NAS 验证。不部署、提交、推送或连接业务实例。
