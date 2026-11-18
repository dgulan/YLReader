# 受限HTTPS CONNECT代理验证

依据browser-assistance-egress-design-2026-11-15.md，新增engine/browserassist/EgressProxy处理器及测试。本轮不创建生产监听器、不启动solver/Chrome、不修改Fetcher/Compose/API或默认抓取。未部署、提交、推送、合并，未访问真实书源或业务数据库。

## 已实现

只接受HTTP/1 CONNECT、精确DNS主机白名单、443及无正文请求。每条新隧道重新解析全部IP，混入任一私网/保留/特殊地址即拒绝，实际TCP拨号使用已经检查的IP。目标域名不会再次交给拨号器解析，也不在失败后自动换IP重试。

握手阶段最多捕获64KiB TLS ClientHello，使用Go TLS解析器检查SNI与CONNECT目标一致；本地TLS错误输出丢弃，不中间人解密、不提供伪造站点证书、不修改真实客户端TLS校验。有效ClientHello之后才拨目标。流量透传，不声称能够检查加密HTTP Host/HTTP2同连接复用或应用层重定向内容。

默认每实例4个隧道、每方向16MiB、30秒生命周期，构造参数均有上限。不排队，容量耗尽返回503；普通HTTP/非授权目标拒绝。绝对socket deadline及context取消关闭连接，双向复制任务都退出后归还名额。未完成ClientHello的连接也会到期关闭，不永久占槽。

## 测试证据

- Windows：21项顶层、含子测试37个通过事件，0失败/0跳过，`.toolchain/browser-egress-windows-complete-20261115.jsonl`。
- Linux integration race：22项顶层、含子测试38个通过事件，0失败/0跳过，`.toolchain/browser-egress-linux-complete-20261115.jsonl`。
- 包含前两轮协议/Gateway回归，两个平台范围有重叠，不相加作为75项独立业务测试。go vet通过。

本地TLS夹具使用测试专属DNS/拨号映射，把模拟的公开IP映射到本机测试TLS服务；生产实现没有这种绕过配置。真实TLS客户端信任夹具证书，验证端到端握手与正文透传；没有用InsecureSkipVerify来取得成功。

覆盖：TLS透传、跨域跳转新CONNECT被拒、SNI不符与明文均不能触发目标拨号、DNS从公开到私网的重绑定、混合DNS结果、IPv4/IPv6/mapped/元数据/CGNAT/NAT64拒绝、非授权authority、普通GET拒绝、容量限制、握手到期释放和超限响应截断。

Linux额外检查在--network none容器中只有loopback接口，直接IPv4/IPv6 TCP及向公开DNS地址的UDP拨号均失败。该容器只运行Go测试夹具，不是真实浏览器；它证明了选定隔离环境的直连边界，不证明浏览器、代理与控制通道已经串接。临时容器无外网、非root、只读源码，正常退出后--rm清理。

## 保留门禁

只配置Chrome代理参数仍可被直连/UDP/QUIC/外部DNS绕过，当前处理器本身不能拦截不经过它的连接。它不得公开成通用代理；承载HTTP服务器必须配置请求头超时/大小限制，并监听私有接口或受权限保护的Unix socket。

下一步为浏览器侧本地TCP到Unix通道与无直连拓扑：浏览器仅loopback/IPC，代理拥有受控出网能力，API使用独立控制路径。必须用模拟站点验证代理访问成功、绕过失败、控制通道不成为任意转发器，再以真实solver检查页面子资源及浏览器退出后的连接回收。跨进程/实例的全局限额、任务生命周期、缓存、实际出口一致性和真实站点仍未完成。

备份65项检查、16份冻结摘要及git diff --check为本轮收尾校验；没有将本轮有限组件验证表述成生产安全或实际过盾已完成。
