# 交接恢复数据核验内部协议 v1

日期：2026-11-16。先行协议；复用已有同次恢复 guard、解密证明和安全文件驱动，不修改公开 API、DDL、冻结备份结构或操作状态。

## 活体守卫与数据核验

只有仍持有租约、验证包、原暂存及 headerPublicationProof 的同次 restoreGuard 可生成内部数据证明。必须同时匹配 operation/plan/package/manifest/owner/staging、运行装配的同一 keyring 指针、manifest KID 清单及 verified publication。新增仅在本 guard 的 PublishObjects 完整返回成功路径设置的内存确认位；publication 写入确认丢失后，即使磁盘 JSON 是 verified 或只读重放可见，也不得补设该位。关闭或重建 guard、只有磁盘 JSON、伪造成功布尔值均不能恢复此能力。

持 guard.mu，重验现有租约、配置路径与目录物理身份、计划期限和明确目标确认。通过目标安全目录句柄只读打开已发布对象根，核对实际树身份与 receipt.TreeFingerprint。逐对象按认证 manifest 比对 stage inventory 的 ID/key/bytes/SHA，使用安全 Get 回读已发布字节，验证长度与 SHA；不使用可覆盖 LocalFS.Put、不复制或修复对象。此检查不枚举额外文件，不宣称树内无额外文件。

复用 validatePublicationDatabase 的只读事务，核对迁移账本、manifest 行数、所有 header 的 AAD/密文认证和 KID inventory、实体/对象引用、会话全部撤销且无 queued/running 任务。此时点在任何恢复后登录/HTTP 行为之前，避免把正常登录新增会话与表计数变化当成恢复失败；后续行为验证必须另行设计先后顺序。

核验后再次重验树身份、配置/租约和三份控制记录摘要。任何错误、取消、计划过期、记录或树变化都返回固定错误，不输出部分成功证明，不写目标/控制记录，不清理、不恢复执行。现有 guard 的协作租约不能阻止恶意管理员或非协作写入；逐项回读不是原子快照或永久数据证明。

## 报告连接

handoverProbes 增加可选 Data 回调，只供内部生产适配器与确定性测试使用。生产适配器必须显式给出完整 guard/stage/owner，且必须绑定同一 Plan、keyring 和 KID 清单，不能从外部 JSON 构造证明。

采集窗口内数据证明绑定本次 plan_digest、三份记录摘要、publication 树身份；只有与采集前后完全相同的 snapshot 匹配，才把 published_objects/header_keyring/sessions_revoked/tasks_disabled 四项由 unavailable 改为 pass。未配置 Data 时保持原有 4 pass / 6 unavailable；本轮完整数据路径为 8 pass / 2 unavailable。

jwt_rotation 和 runtime_behavior 继续 unavailable：不同内存 JWT 不等于旧令牌拒绝，数据检查不等于 HTTP 行为测试。不能通过 validateFor 的十项全通过门禁；不实现 operation CAS，不推进 ready_for_cutover/completed，不部署或常驻启动。

## 验收

单测证明绑定、可选 Data、失败/取消/漂移及闭合守卫拒绝。扩展既有真实 pg_dump/age/事务/header/对象发布隔离夹具，在 headergood、HTTP 登录之前跑生产适配器及安全报告保存；注入坏对象、坏 header、重新激活会话、重新排队任务与错误 keyring 验证拒绝，再验证仍可成功。所有破坏仅发生在本轮自有夹具，由测试恢复原值，不由探测器修复。保留既有真实恢复与 HTTP 回归，不把未运行项目计为通过。
