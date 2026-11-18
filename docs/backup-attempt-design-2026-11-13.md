# 隔离恢复排他与尝试记录 v1（2026-11-13）

这是 ADR-019 实现层的内部格式，不增加 HTTP/CLI 操作或改变冻结的 plan/operation 字段。实现前确定以下约束。

- 在 Linux 本地文件系统上，对已打开的 target/control 目录分别取非阻塞 flock 排他锁；不在目标内创建锁文件。锁 FD 独立、CLOEXEC，进程退出由内核释放。只有具备此能力的驱动可继续；不提供 Windows 或远程文件系统降级。
- 加锁后重新验证计划、确认值、包绑定、验证预算、目标 DSN、数据库空态和五个配置位置；保留原目录句柄及锁直到本次准备句柄关闭。配置路径被替换必须拒绝，不能仅凭旧 FD 的身份不变认定路径未漂移。此锁只排除配合的进程，不能防止同权限用户或管理员主动篡改。
- 独立内部记录 `operations/<operation_id>/attempt.json`，严格 JSON，字段为 attempt_format=`ylreader-restore-attempt`、attempt_version=1、operation_id、backup_id、plan_digest、owner_id、target_storage_fingerprint、generation_reference、database_outcome。全部必填；generation_reference 为 null 或 `.scratch-` 加 32 个小写十六进制字符；database_outcome 为 not_started、unknown、committed、rolled_back。
- 初始记录 generation_reference=null/outcome=not_started，CreateOnly 发布。登记 generation 必须先于任何对象字节写入；登记只能由 null 变为一次已知引用。若进程恰在目录创建与登记之间退出，可能留下空私有目录，不能自动推断其归属或删除。
- 执行数据库步骤前先 CAS 记 unknown；只有得到明确提交或确认回滚结果后才能转 committed/rolled_back。unknown 不得解释为失败后可删除，committed/rolled_back 是不可逆结果，不能再次执行数据库步骤。同一记录重写只可用于返回既有结果或无变化 CAS，不能更换 owner、计划、包或 generation。
- 记录是状态/位置证据，不是 apply 授权；读取既有记录不重放操作。若记录写入结果未知，调用者必须重读，不能盲重试。中断后保持现场，不根据陈旧心跳自动取得所有权、续跑或清理。
- 保留结果未知的暂存目录时仍须关闭该暂存区的目录及父目录句柄；关闭不删除任何文件，后续不能使用该旧句柄清理。
- 本轮可实现并测试准备句柄及登记能力，整体 apply 必须继续接入对象发布、真实 header 解密、完整状态流与故障收敛后才能开放。

## 数据库接线补记

对象已暂存时，用保留的目录/租约重验身份与新打开的配置路径，目标允许非空但只能包含登记的 generation，且该目录身份和 owner 记录必须匹配。重新检查计划有效期、确认值、包/操作绑定、取消标志和真实数据库空态；不依赖旧的只读检查结果。

数据库写入前先持久 CAS unknown。该 CAS 返回任何错误时不执行数据库步骤；当前准备句柄也不允许盲重试，即使重读暂时仍显示 not_started。已是 unknown/committed/rolled_back 的请求只返回状态，不重放。使用读取并验证过的同一 DSN 进行实际数据库调用，不在写入前悄悄换读另一个目标。

真实数据库明确提交或确认失败后，以独立、限时上下文登记 committed/rolled_back。若结果登记或后续 operation 状态发布失败，保留对象、报告结果记录未确认，不降级为可清理失败。只有 rolled_back 已持久登记且数据库明确没有提交，才允许本轮对象清理。成功仍停留 validating，不产生 ready_for_cutover/completed。新进程没有自动接管或续跑能力；unknown 必须只读核对并人工处理。
