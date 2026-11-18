# 隔离审阅报告与操作版本登记内部协议 v1

日期：2026-11-16。先行定义本轮实现；不改已有冻结 report/operation 字段、API、DDL 或状态语义。只登记 isolated-http-fixture-v1 同次报告，真实用户通用运行器、生产入口及服务切换不在本轮交付范围。

## 活体能力与确认

同次 live guard 完成行为终结后保留私有最终报告摘要和已知会话增量。新增 guard 保存方法仅在现有排他保存/回读/记录稳定检查全部成功时记录内存 persisted=true；任何写入确认丢失、冲突或回读失败都不授予此能力。磁盘报告可见不替代确认，重建 guard 不恢复能力。

登记前必须重读完整十项报告并匹配内存摘要、旧 snapshot 与计划，重跑三类身份探测并与报告摘要一致，再以同次会话增量重验对象/header/安全状态。Data 回调不得被当作新的原始行数检查运行在行为之后。注册尝试在 guard 内一次性锁存；失败后只允许诊断，不自动续跑。

## 三阶段记录与 CAS

控制区新增 `operations/<UUID>/handover-registration.json`，全部字段必填：registration_format=ylreader-handover-registration、registration_version=1、scope=isolated-review-registration、operation_id、backup_id、owner_id、plan_digest、report_sha256、base_state_sha256、base_generation、next_state_sha256、next_generation、recorded_at、state=prepared/registered、ready_for_cutover=false、deployment_authorized=false。限制 8KiB，严格拒绝重复/null/未知字段、非规范 UTC、非法 UUID/SHA 与非连续版本。

1. 只在报告尚有效、旧 operation 仍 validating/未取消、三份记录与报告绑定一致时，以 CreateOnly 写 prepared。报告使用磁盘原字节 SHA；新 operation 以标准 JSON 编码摘要，保留 identity/owner/plan/phase，generation+1，updated_at/heartbeat_at=recorded_at，使用既有 ValidateTransition。
2. 使用旧 state 原字节 SHA 对 state.json 执行一次 CompareAndSwap。不能仅按 generation 重读后盲写；不能推进 ready_for_cutover 或 completed。明确冲突/忙返回冲突，任何 ErrCommitUnknown（包括与冲突同时存在）优先为未知。
3. 回读确认新 state、报告原摘要和未变的 attempt/publication，报告仍有效；再用 prepared 原字节 SHA 将登记记录 CAS 为 registered。回读确认登记/状态与输入均匹配，才返回成功。

步骤 3 的登记 CAS 即使明确返回冲突，也统一视为结果未知：此前 state CAS 已成功，不能把它归类为无副作用的普通冲突。prepared 和报告均保留，不倒退操作版本。

prepared 的 identity/digests/generations/timestamp 不可更改，只允许 prepared→registered。报告的 observed_generation 与原状态一致；登记记录明确连接旧报告和下一版本，不改旧报告、不伪装成报告直接绑定新 generation。阶段始终 validating，所有部署/切换标记仍 false。

## 失败和边界

prepared 写入未知、state CAS 未知、确认回读失败、登记 CAS 未知、保存后状态变化或报告过期都保留所有记录；state 可能已加一，不回滚、不删除、不自动重试。即使只读看到 registered，也不能把先前持久确认丢失改称成功。重复调用同 guard 不再写；新的 guard 无活体能力，只能诊断。

三份文件不是单个跨文件原子事务。prepared/registered 是可核对的日志和确认协议，state CAS 是实际版本原子比较点；多个文件的最终读取不是原子快照或 ABA 防护。当前只在同次 guard 保持租约、无业务流量的隔离环境工作，不能防御管理员/非协作修改或最后读取后的变化。

## 验收

单测严格协议、陈旧/部分报告、并发创建、state 冲突、state/登记确认丢失、回读破坏及保留。Linux 安全文件 tmpfs 验证并发与实际 CAS 后确认丢失；真实归档/HTTP 全链在同次十项报告后登记，核对 generation+1、phase=validating、绑定摘要与重复调用不重放。tmpfs 不等于断电持久性或 NAS 验收；不部署、提交、推送。
