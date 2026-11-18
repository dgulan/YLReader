# 一次性行为派发及会话响应记账契约

先行内部协议；不增加公开CLI/API，不修改冻结归档/DDL/报告profile。

## 受控入口

live restoreGuard内部入口在任何核验前锁存本进程的单次尝试，核验失败也不自动重试。复用verifyBehaviorAccounts取得同guard/plan/consent/控制记录/用户/目标数据库/运行修订的只读观察，随后持有guard锁，重验租约、物理绑定、操作版本及有效期，再执行既有recordBehaviorIntent。每个operation的唯一意向槽阻止新guard或更换consent重复登记；写入未确认不调用登录。

运输层为内部可信接口，必须绑定target_instance_id/database指纹/runtime修订，不能由外部JSON、URL或回调注册。当前只提供派发内核与隔离测试适配器；实际HTTP运行器尚未提供。未来适配器必须对每次连接核对目标pin/角色、禁用重定向和自动重试、处理自身认证会话秘密，不能仅靠声明的Binding证明远端。全流程不产生正式发布或恢复执行许可。

账号按预检顺序串行，每账号最多一次Login；不调用Refresh。整体派发上下文最多20秒，观察超过20秒或consent过期停止。每次动作前重验guard/控制版本/取消/时间。并发调用仅一个进入核验；后续同operation不覆盖意向。

发送给运输层的credential副本仅保留目标登录所需的user_id/username/password，OldAccess/OldRefresh清空；原始预检输入保持不变。旧源令牌和JWT密钥不交给登录运输层。

## 不可覆盖动作记录

路径固定 `operations/<op>/behavior-actions/<index>/dispatch.json|response.json|logout.json`，index为0..7。记录最大8KiB，严格JSON包含action_format/version、operation_id、consent_id、intent_sha256、index、user_id、kind、session_id、parent_sha256、observed_at。dispatch parent为意向摘要；response parent为dispatch摘要；logout parent为response摘要。不含密码、token、用户名、DSN或凭据路径。

dispatch kind=dispatch_reserved，session_id为空。create-only及回读确认完成后才调用Login；确认丢失/冲突/绑定变化不派发。Login返回错误、超时或无有效的同用户会话UUID时，尝试记录login_response_unknown并停止，不自动重试、不猜测会话、不对外来会话执行Logout。

只有收到有效同用户会话UUID，才记录session_observed并对该会话最多调用一次Logout。会话UUID不能与本轮其他已知会话重复。Logout响应kind为logout_response_observed或logout_response_unknown；这些名称只代表适配器响应声明，**不证明数据库会话已撤销**，后续仍须真实数据核验。任何结果写入不确定或Logout失败均停止后续账号。

操作员意向明确允许“已知同用户会话的单次登出”，包括父context取消后用独立2秒上下文执行的受限清理；不会再次登录、撤销未知会话、扩大到该用户全部会话或删除行。已知会话的响应/登出记录也可在此独立清理上下文中写入，确认丢失不重放。若response登记失败仍可对内存中已知会话尝试一次登出；logout记录可能留下未确认父记录，必须按结果未知核对。

所有写入错误返回固定文本，持久记录保留。已有意向诊断继续保守报告outcome_unknown/not_inferred，即使本轮会话响应均可见也不从磁盘重建派发能力。本轮不扩展诊断CLI或将通用响应标为既有合成报告pass。

## 验收边界

单测用可信假运输层检查并发/重放、0/1调用次数、错误与取消、写确认丢失、错误/重复会话、关闭guard及凭据不泄漏。独立真实归档恢复子夹具通过内部AuthService适配器登录已恢复的合成账号、按refresh摘要取得确切session UUID并仅登出这些会话；断言新增会话有界且已撤销、旧行不删除、无后台抓取。该测试适配器不发布为生产运输层，不使用真实用户账号。
