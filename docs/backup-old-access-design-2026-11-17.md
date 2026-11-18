# 旧 access token 受限 HTTPS 探测

内部设计先行；不修改冻结 API、归档、派发账本或部分报告 profile。

提供具体 behaviorHTTPTransport 的显式内部方法，只对其已有用户白名单发送一次 GET /api/v1/auth/me。输入旧 access、用户 UUID、token_version 与源 JWT 材料；发送前验证源签名、用户、版本、当前有效期，且源/目标密钥必须不同、目标签名必须失败。禁止携带密码或 refresh token；不调用 refresh/logout/login，不接收任意 URL。

复用固定叶证书、loopback、禁代理/重定向/连接复用的客户端。每用户发送前锁存一次尝试，失败、丢响应或取消后不重放。只接受 401、application/json、严格符合现有 Error 模型且 code=unauthorized 的有界响应；网络错误、403、重定向、畸形、压缩或超限响应均失败。关闭响应失败也不能确认。错误不含秘密，结果不落盘、不形成运行见证或执行权限。

调用者必须显式调用；不自动插入现有登录派发，不扩展原 consent 的执行范围。当前集成仅在隔离合成夹具调用。发送前及响应解析后均复核源令牌有效性；HTTP 401 不单独证明具体拒绝原因。原 old_token_http_rejection 同时要求旧 refresh 等证据，因此仍 unavailable，固定 10/2 不变。

本轮测试覆盖请求固定性、源/目标密钥与用户/版本/时效、仅一次尝试、响应状态/格式/长度/关闭失败及真实 HTTPS API 的旧 access 拒绝。生产服务生命周期、公开入口、旧 refresh 和阅读证据另行设计。
