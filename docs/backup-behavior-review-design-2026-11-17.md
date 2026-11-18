# 通用HTTPS账号行为部分审阅协议

先行内部协议；不修改冻结API/DDL/归档、旧handover-review或isolated-http-fixture-v1语义。

新profile固定 `bound-https-account-verification-v1`、scope `localfs-bound-https-account-review`，status恒partial。十二个固定检查：schema_ledger/database_identity/database_role/published_objects/header_keyring/redis_isolation/sessions_revoked/tasks_disabled/credential_preflight/https_login_logout为pass；old_token_http_rejection/reader_roundtrip恒unavailable。只覆盖当前可证明的最小集合，不将登录登出当作阅读验收。execution_authorized/ready_for_cutover/deployment_authorized恒false。

## 同次运行见证

只有具体behaviorHTTPTransport的登录响应经JWT+固定数据库会话核验、Logout经204+数据库revoked核验，且派发内核的意向/各动作写入全部成功确认，才在原live restoreGuard内建立见证。见证绑定guard/plan/consent、账号UUID/确切session UUID、三控制记录、完整账本摘要、目标DB/运行修订/Redis身份及TLS证书摘要、起止时间。不可序列化，不从JSON或数据库重建。运输层完成历史仅用于当次捕获，Close清除本地历史；guard.Close清除见证。

服务测试适配器、派发/响应写确认丢失、重定向、证书错误、丢响应或部分账号均没有此见证。磁盘完整记录与当前revoked不能替代历史确认。原账号观察需保留同次Redis身份，不能仅按文本profile升级。

派发前复制源/目标JWT材料用于同次核验，目标快照还须与运输层实际验签密钥一致；采集时逐字节比较输入，不能保留同一个修订UUID却换密钥。此材料只存私有内存，不做秘密摘要或序列化；采集成功、失败派发或guard.Close清除拥有的字节副本，不承诺库/字符串副本安全擦除。

## 采集与保存

采集只读：要求同guard/Stage/Owner及显式身份输入；验证见证与稳定账本逐用户/会话精确一致，重验数据库/运行装配/Redis身份，做会话只读复核，再在live guard下复核对象/header/账本/角色/任务及受限会话增量。会话增量仅允许本轮确切UUID、同一组用户且全部revoked，创建时间在意向前最多1秒至本轮结束；不允许其他会话或后台任务。

数据库复核仍要求无其他同库连接，可信调用者须先暂停/关闭本轮自有HTTP服务和连接池。采集器不自行关服务、不登录/登出/修复。运行修订仍是显式内存装配，不升级为正在运行实例全配置实测。前后重读账本及guard绑定，变化、过期、取消、身份不符或未知活跃会话一律失败，不产出部分成功报告。

报告包含format/version/profile/scope/status、operation/backup/consent/plan/package/manifest/target/storage/tree、generation及三记录摘要、目标身份三摘要、TLS叶证书SHA256、完整账本SHA256、账号/会话数、started/finished/expires、固定checks和三个false授权标记。无用户名、session/user清单、端点/DSN、密码/token/密钥。有效期不超过意向、计划或结束后5分钟。

保存路径 `operations/<UUID>/behavior-account-review.json`，最大32KiB严格JSON/create-only。只能保存当前guard刚采集的准确报告摘要，一次保存尝试前锁存，回读并重验账本/时效；确认丢失保留且不重放。不改operation generation/phase、不CAS登记到旧报告，不授予后续执行/切换许可。读取文件只是一份部分审阅声明，不能重建见证。

失败的纯只读采集可在原见证有效期内由调用者明确再次观察，不自动循环；首次成功报告固定后不能替换。此行为不重试登录或保存，也不能弥补缺失的运行见证或写确认。

测试覆盖跨operation/consent/账本/身份/证书/用户/会话、过期、缺见证、畸形报告、保存冲突/确认丢失/回读失败及重放；真实TLS归档夹具只有完全确认的good场景可采集保存，其他场景拒绝。后续阅读与旧令牌HTTP拒绝需另行实现授权行为和证据，不补造pass。
