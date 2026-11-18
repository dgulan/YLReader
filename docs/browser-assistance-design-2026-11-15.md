# 书源验证页诊断与可选浏览器辅助契约草案

先确认失败原因，再增加依赖。仅诊断实现属于本轮；不部署/安装FlareSolverr，不修改默认抓取通道、公开API/DDL、worker模式或认证凭据。本设计不是已经接通过盾或保证指定站点可用。

## 官方依据与修正

- Cloudflare将cf-mitigated: challenge作为验证页标志，不能仅用cf-ray、Server或403判断：https://developers.cloudflare.com/cloudflare-challenges/challenge-types/challenge-pages/detect-response/
- clearance与访客/设备绑定，可能在过期前被重新质疑；Cookie+UA+相同IP不是Go直连必定成功的证明：https://developers.cloudflare.com/cloudflare-challenges/concepts/clearance/
- FlareSolverr说明需匹配其UA；request.get默认maxTimeout60000ms、会话需要销毁，服务不能公开到互联网：https://github.com/FlareSolverr/FlareSolverr

## 本轮可实现诊断

优先识别任意状态的显式cf-mitigated: challenge。保守回退只针对403/503、Server为cloudflare、且正文含技术性挑战路径/cf-browser-verification，或验证提示组合；已声明非HTML类型不使用正文回退。cf-ray、普通403、单独Just a moment文本均不触发。正常响应沿用原有完整大小与编码约束，回退最多检查前64KiB；5xx只查看原本丢弃的前4KiB，因此回退可能漏检，不宣称全覆盖。

识别后关闭响应，立即返回固定ErrChallengeRequired并包装既有ErrUpstreamError；不把页面交给正文/搜索解析器，也不对503验证页重复直连。现有API code仍为source_request_failed；搜索failure的安全文案可明确要求浏览器验证，不输出HTML、Cookie、目标URL或cf-ray。普通503重试、SSRF/DNS/重定向过滤、POST不重放与限流均保持。

## 后续辅助器接入门禁（待实现）

1. 服务级总开关默认off；管理员配置可选auto，always不进入首版。不能仅凭书源JSON开启浏览器、指定solver地址或代理。源级只能在管理员允许范围内进一步关闭。
2. 先直连并确认挑战，再按授权origin启用辅助；首次只支持GET、至多一次求解及一次直连重放。POST、表单提交、登录、付费或交互验证码不自动重放。浏览器导航本身仍需站点授权及URL范围约束。
3. 当前搜索HTTP预算10s，不能同步嵌入默认60s求解；初次请求可明确返回待验证，独立有界任务由自己的context/全局并发和TTL管理，不悄悄延长搜索或后台无限运行。如何暴露状态/触发任务须另行冻结接口。
4. 缓存键按租户/用户、书源、规范化精确origin（scheme/host/port）、出口配置版本隔离，不能只按注册主域共享。允许共享的公共站点需显式授权；不合并多租户登录Cookie。Cookie遵守Domain/Path/Secure，不向跨域重定向发送。
5. 仅缓存允许范围的clearance及必要元数据；UA与凭据成对，TTL取实际Cookie到期减余量与运维上限最小值，不能硬编码15–25分钟保证有效。过早挑战则失效一次并冷却，禁止死循环求解。敏感值加密存储、不进日志/审计包。
6. singleflight仅进程内；多实例需要Redis锁（随机owner、TTL、有界等待、比较owner释放/续租）、结果再次读取。等待者取消不能直接取消其他使用者的共同任务；全局浏览器并发独立限制（初始1），并加失败退避/熔断。
7. 最重要的新增SSRF边界：浏览器会自主请求重定向、iframe、JS、子资源、WebSocket及下载，Go首次URL校验不能保护这些请求。必须由独立出网策略/代理限制所有浏览器流量，拒绝私网、链路本地、元数据、IPv6特殊网段及DNS重绑定，且覆盖目标端口。仅同一个Compose网络不等于相同出口或安全隔离。
8. 不转发用户Authorization或书源登录Cookie给solver，不对外发布8191，不暴露Docker socket；固定镜像版本/摘要、限制CPU/内存/pids、私有临时配置与有界会话生命周期。出口一致性要实测，不能用关闭Go TLS校验或SSRF过滤来实现。

必须先通过模拟solver、租户隔离、Cookie范围、取消、超时、并发、锁过期、重放次数、浏览器内网子资源/重定向拒绝与资源回收验证，再测试真实站点。直连重放仍被挑战时应返回明确失败；是否允许持续浏览器通道须另议，不能自动无限升级。
