# 恢复后 HTTP 隔离验收协议（2026-11-14）

本轮仅扩展隔离实验验收，不增加公开 API、备份字段或恢复状态。前置条件是同一次实验的数据库 committed、header 完整核验及对象 publication verified；仍保持 validating，不产生 ready_for_cutover/completed，不启动常驻服务或部署。

使用真实路由、服务、PostgreSQL、已发布对象根和临时 HTTP listener。fixture 在归档前使用现有 Argon2 参数和认证服务生成可登录密码及真实旧 access/refresh token；目标独立生成随机 JWT 密钥。先检查旧 token 拒绝，再登录，避免 refresh 重放防护撤销新会话而干扰验收。同时复用旧 JWT 密钥进行一次服务层反例检查，确认 token_version 自身也能拒绝旧 access token。

源和目标 Redis 是两个本轮自有的独立进程，均在无外网的测试网络命名空间中、使用 tmpfs、无宿主端口。显式配置固定实验端点，不读取环境中的业务 Redis 配置，不使用内存缓存降级。通过同 key 的独立值验证双向隔离，不执行 FLUSH 或删除其他 key。本地书正文绕过 Redis，因此正文成功本身不能作为 Redis 隔离证据。

HTTP 检查健康、数据库/结构/Redis readiness、登录、书架、目录、正文、进度和书签，以及跨用户拒绝。外部抓取器一旦被调用即报错并计数；后台 worker 不启动。目标登录会创建新会话并更新 last_login_at，故归档行数核验必须在 HTTP 之前完成；这不是恢复数据损坏，也不能再以原 sessions 数量检查运行后的数据库。

只有所有断言成功，测试才输出一份脱敏 JSON 实验证据；脚本保存到本轮独立目录并校验恰好一份、固定检查集合全部成功。证据绑定 operation、plan、package、manifest、target 和 publication 身份，范围固定为 isolated-http-fixture，deployment_authorized 和 ready_for_cutover 均为 false。它是已经发生的合成实验记录，不能充当真实环境切换授权、持久恢复状态或 CLI 交接凭据。不得输出凭据、JWT、refresh token、header、DSN、正文或宿主路径。

实验结束只清理标签匹配的本轮容器。真实设备、合法书源、S3、大数据性能及常驻实例的最终交接门禁仍须另行验收。
