# 浏览器辅助HTTP Gateway验证

本轮实现browser-assistance-gateway-design中的有界HTTP控制通道，仍未在cmd/server、Fetcher或Compose注册。未启动FlareSolverr或真实浏览器，未改变书源、数据库、认证、默认抓取与公开契约；未部署、提交、推送或合并。

## 交付

NewHTTPGateway固定管理端配置的/v1地址、明确端口、私有/回环控制IP白名单及允许目标origin。每次控制DNS解析全部结果必须在白名单内，然后按解析后的IP拨号；不把目标域名重新交给Dialer解析。不读环境代理，保留默认TLS验证；禁用keepalive/自动压缩与重定向，避免控制POST重放。

请求只接受request.get/url/maxTimeout三个字段，最多32KiB，拒绝未知/重复/大小写变体字段。目标先做授权origin与全部DNS地址检查，复用Fetcher私网/特殊地址判定。maxTimeout按剩余预算缩短；总预算默认5秒、上限30秒，覆盖DNS、拨号及正文读取。响应头32KiB、正文最多调用方限制且不超过1MiB，只接受HTTP200和无压缩/identity；错误不包含地址、响应或原始错误文本。

这只是初次目标检查。浏览器后续DNS解析、重定向、iframe、脚本、子资源和WebSocket尚未经过强制出网代理，不能据此宣称浏览器SSRF防护已完成。Headers的后续重定向范围校验、实际出口一致性和远端浏览器生命周期仍是接入门禁。

## 结果

Windows和Linux race均14项顶层、含子测试30个通过事件，0失败、0跳过。范围含上一轮协议组件，不能将两个平台相加为60项独立业务测试。证据：

- `.toolchain/browser-gateway-windows-complete-20261115.jsonl`
- `.toolchain/browser-gateway-linux-race-20261115.jsonl`

测试覆盖协议组件到模拟HTTP服务的往返；请求无认证/Cookie；配置拒绝；目标与控制DNS混入私网/白名单外IP；307不跟随；503不重试；大Content-Length、分块超长、压缩拒绝；TLS自签证书拒绝；DNS预算到期；正文取消及服务器连接退出；拨号IP固定与错误脱敏。go vet通过。Linux为--rm、无外网、只读源码、非root的临时测试容器，未安装或运行solver。

首轮取消夹具没有消费POST正文即等待服务器context，导致无法可靠观察断连并卡在httptest关闭；原记录保留于browser-gateway-windows-20261115.jsonl。定位并终止了本轮Go临时构建目录中的测试进程，夹具改为先读完JSON、用启动事件同步取消并保证失败时也能释放，随后两平台通过。没有放宽生产取消/超时检查。

备份65项检查、16份冻结摘要和git diff --check作为收尾校验。没有重跑全量浏览器/真实书源、solver或生产镜像扫描，不将这些未执行项标成完成。

## 下一步

实现浏览器强制出网代理与网络拓扑隔离：逐次解析并固定公开IP，拒绝私网/特殊网段、跨越允许范围的跳转/子资源及DNS重绑定；从网络层禁止绕过代理。然后再测真实solver取消后的资源回收、缓存/共享任务生命周期，最后才评估受控真实书源。当前HTTPGateway不得单独作为开启自动过盾的依据。
