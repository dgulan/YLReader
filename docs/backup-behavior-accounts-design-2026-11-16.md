# live guard 下的通用恢复账号核验

先行内部契约，不变更冻结API/DDL或isolated-http-fixture-v1报告。此次交付只读账号核验，不派发登录、不创建会话、不发放执行许可。

输入必须为既有preparedBehaviorInput及显式handoverProbeInputs；Guard/Stage/Owner/目标实例必须与同次恢复一致。先复用已有数据库/运行修订/Redis/对象/header核验生成8项部分审阅，要求jwt_rotation/runtime_behavior仍unavailable、其余pass，且consent绑定部分审阅的同一控制版本和运行修订。部分报告不保存或改写。

持有live guard互斥，重验文件物理身份/租约、原计划及控制记录后，只使用明确隔离DSN建立一个独立连接，在READ ONLY、REPEATABLE READ事务内核对系统标识、数据库OID/角色OID、数据库归属/角色权限/无角色继承/无额外同库连接、PG16及冻结迁移账本。来源和目标数据库pin必须不同。固定SELECT，无SQL修复、会话撤销或权限提升。

同一事务内按授权UUID核验：账号未禁用、username精确相同、密码通过现有有界Argon2校验；旧access须HS256、原JWT密钥签名、subject正确、必须有iat和exp且当前有效，旧token_version恰为恢复值减一；旧refresh须在目标表中属于同一账号、已撤销且当前未过期。源/目标JWT分别32..4096字节且必须不同。refresh仅为查库计算已有协议要求的SHA256，不持久化/输出此摘要。用户名、hash、密码、token、DSN均不进入证明或错误。

事务提交前以同一时钟再次校验所有旧access/refresh及consent有效期，避免耗时密码核验期间过期被当成撤销成功；事务后重验guard与控制版本。连接/事务关闭使用有界独立清理上下文，所有错误固定文本。最多20秒上下文；Argon2单次运算不能被context硬中断，其资源上限沿用service.VerifyPassword。

返回内部、不序列化的账号核验观察，绑定guard指针、plan、consent、三记录摘要、目标数据库fingerprint、运行修订、授权user UUID及有效期。它不是behaviorGrant，不改变任何审阅pass或operation状态；后续派发仍须临近执行重新校验、持久唯一意向、一次性能力和会话结果记账。没有旧有效凭据则本次严格核验拒绝，不能用过期当撤销；支持降级为unavailable的用户流程留待后续明确。

验收使用真实pg_dump/age恢复和合成账号，覆盖正确/错误口令、username、disabled、过期/错误签名/subject/version access、错误/未撤销/跨用户refresh、相同JWT、错误pin/runtime/owner、关闭guard及只读不变性。合成账号用于测试通用代码，不代表已获得真实用户授权或完成生产验收。
