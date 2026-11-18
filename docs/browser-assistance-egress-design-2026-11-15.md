# 浏览器HTTPS出网代理实现范围

本轮增加未接入生产的HTTP/1 CONNECT处理器，不部署真实浏览器。仅允许管理配置的精确DNS主机和443端口；拒绝IP字面量、userinfo、非CONNECT及正文。逐次解析全部DNS，任何私网/保留地址均拒绝，拨号只用已检查IP，不重新按域名解析。

CONNECT后最多读取64KiB TLS ClientHello，SNI必须与授权CONNECT主机一致；不解密业务TLS，不关闭客户端证书验证，未带有效ClientHello的明文请求被拒绝。TLS内部Host/HTTP2复用不可由该代理检查，不把TCP/SNI边界宣传为完整HTTP应用origin隔离。

每实例最多4个隧道（可配置1–16），不排队；默认30秒生命周期、每方向16MiB，受取消和绝对socket deadline约束，所有复制任务退出后释放名额。不解析/记录Cookie、请求正文或站点HTML，错误固定文案。

必须配套无直连的网络边界：浏览器仅loopback/IPC或只能到代理的防火墙规则，禁止直连TCP/UDP、QUIC、外部DNS与IPv6旁路。只设置浏览器proxy参数不构成强制隔离。本轮通过Linux无网络容器检查直接IPv4/IPv6/UDP不可出网，但不把Go客户端夹具写成真实FlareSolverr/Chrome已配置完成。

承载服务器必须配置ReadHeaderTimeout、MaxHeaderBytes且只监听私有接口/Unix socket，不公开成通用代理。实际浏览器的本地TCP转Unix通道、控制平面连接、外部网络拓扑与真实子资源验收继续保留门禁。模拟验收覆盖TLS隧道、SNI不符、明文拒绝、跨域重定向、DNS重绑定/混合结果、拒绝特殊IP、资源释放及网络none直连拒绝。
