# 浏览器辅助协议适配器 v1（实现前记录）

本轮仅增加engine/browserassist隔离协议组件和模拟验证，不注册API、不配置solver地址、不修改Fetcher/Compose、不启用浏览器。实际网络Gateway和浏览器全部出网约束仍待实现，因此组件本身不是SSRF防火墙，也不能在生产接管抓取。

Gateway仅接收结构化request.get请求及最大响应预算；由未来可信管理层提供，必须执行context、限制响应读取并落实浏览器全部请求的出网策略。适配器不接收HTTP请求头/认证/Cookie/代理参数，仅同一HTTPS精确origin的GET URL，拒绝userinfo、fragment、IP字面量和非标准端口。DNS/子资源仍必须由实际Gateway解决，禁止把本轮语法检查写成完整SSRF验证。

默认5秒总预算（含排队），上限30秒，每个适配器实例至多一个调用，超时/失败不自动重试，不创建遗留执行goroutine；不响应context的Gateway在返回前继续占用名额。每次只发送一次request.get，绝不POST原站或透明重放原请求。本轮不实现缓存、singleflight、Redis锁或跨实例全局限额。

读取完整FlareSolverr响应但最多1MiB，要求status=ok、solution状态2xx、最终URL仍在授权origin、有效UA及唯一cf_clearance。只接受同一精确host、Secure且Path合法的cookie，拒绝过期/无到期时间/重复clearance；其余cookie丢弃。有效期取实际到期减30秒与本地10分钟上限的最小值。响应重复JSON键、过深JSON及缺字段失败关闭，不回显原文。

凭据绑定用户/书源/origin/出口版本，用规范化后结构的SHA256作隔离键；外发前再次匹配这些身份、目标origin、路径和到期时间，生成UA与Cookie配对头。结构字段不导出，默认JSON不泄露值，String/GoString脱敏。该实例内凭据不是已加密Redis缓存；不得假装已具备跨实例共享或出口一致性证明。

Headers只验证这一次目标，不会拦截调用方HTTP客户端自行跟随的后续跳转。将来接入抓取器必须逐跳重新校验scheme/origin/path或剥离凭据；不能因为首次Headers成功就跨域或降级HTTP透传Cookie。

模拟验收必须覆盖：有效协议、同源路径、跨租户/书源/出口/域/路径拒绝、重定向跨域、Cookie过期/缺失/重复/不安全、缺UA、超长响应、重复字段、取消、排队和单次调用。生产接入继续以browser-assistance-design的全部门禁为前提。
