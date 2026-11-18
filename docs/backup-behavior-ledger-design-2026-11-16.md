# 动作链只读诊断与会话撤销观察协议

先行内部契约，不改变冻结API/DDL、既有意向诊断或报告profile；不提供登录/登出/重试/修复入口。

只通过Get读取两轮control/attempt/publication、可选behavior-intent及0..7共24个固定动作槽，比较存在性与原始字节/摘要。每动作最多8KiB、意向16KiB，只有ErrNotFound视为缺失；权限/关闭/超限/JSON错误不可转成缺失。无目录枚举、额外路径或临时文件。重复读取不保证原子快照/防ABA。

固定槽之外的文件不枚举，不能据此声称控制目录没有其他文件。时间过期但版本/绑定仍一致的意向可做显式只读数据库观察，账本仍stale；不会因观察而恢复其有效期。

核对原始父摘要、同operation/consent/index/user、合法kind/路径、会话唯一性及时间顺序。dispatch须在意向窗口内；已知会话的response/logout允许在窗口后记账。所有观察不得在诊断now之后。超出用户数的槽、缺dispatch、错父/绑定、未知response却出现logout、重复会话或前一槽未完成就启动下一槽均拒绝。response缺失但存在logout是既有合法失败窗口，标记parent_unconfirmed且不从孤立logout提取可信session。

全部用户有session_observed及logout_response_observed时仅为responses_observed，不能证明数据库已撤销或历史写确认；其余有效前缀outcome_unknown。缺意向/读取中变化/陈旧/矛盾/不可用单独区分。输出聚合数，不输出用户/session清单或原始记录，dispatch/retry/deployment始终false，persistence_confirmation始终not_inferred。

会话数据库观察另需可信调用者提供原Plan、独立source/target数据库pin及显式隔离DSN。计划/意向/当前控制关联须匹配，目标配置字段须匹配Plan。沿用READ ONLY/REPEATABLE READ事务、数据库/角色/冻结账本校验，不调用认证服务。只按完整response链中的确切session UUID查询user_id、revoked状态，过期不能算撤销；不存在、owner不符、仍活跃分别计数。

对授权user UUID仅统计额外活跃会话数量，发现未知会话不把其ID反填账本，不登录或自动撤销。所有known session已撤销也不排除响应丢失后的未知会话；零known不是成功证明。数据库前后再次读取完整链，变化/取消/关闭失败不返回已核验结论。输出数据库观察与账本声明分离；不证明运行HTTP实例配置，不生成行为grant、报告pass或切换许可。

测试覆盖合法成功/丢响应/写确认丢失、父链缺失/伪造/重复/越界/乱序、文件变化/不可读，以及真实隔离数据库的已撤销/活跃/缺失/异用户会话和未知活跃增量。不修改真实用户数据，仅在自有合成夹具施加并恢复反例。
