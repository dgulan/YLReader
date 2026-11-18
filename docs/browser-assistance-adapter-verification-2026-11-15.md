# 浏览器辅助协议组件验证

依据browser-assistance-adapter-design-2026-11-15.md，在backend/internal/engine/browserassist独立实现。未修改Fetcher、API、Compose、默认抓取模式或数据库；未安装/运行FlareSolverr、提交、推送或部署。Gateway只有接口和模拟实现，没有生产网络实现或出网隔离证明。

## 已交付

- 仅编码request.get、目标URL、受context约束的maxTimeout，不携带用户认证头、原站Cookie或代理参数。默认5秒含排队，总上限30秒、单实例一个调用。失败不重试，也不重放原站POST。
- HTTPS精确origin、端口、userinfo、IP字面量及fragment校验；仍不代替DNS和浏览器子资源/重定向出网过滤。
- 回复1MiB预算及重复键（含大小写变体重复）、深度/节点限制。要求ok/2xx、最终同源URL、UA、唯一有效Secure clearance；按Cookie实际到期减30秒与10分钟上限取最小值。
- 凭据绑定用户/书源/origin/出口版本；每次取请求头检查作用域、路径、有效期，UA与Cookie配对。默认JSON为空对象、常见String/GoString输出脱敏，错误不携带辅助器原始文本。
- Gateway不响应取消时不放弃goroutine或提前释放名额；后续等待者可取消/到期，第一调用真正返回后才允许新调用。

## 测试

Windows定向测试、Linux race测试均为5项顶层、含子测试16个通过事件，0失败、0跳过。覆盖正常协议、跨用户/源/出口/域/路径拒绝、到期、UA注入、跨源最终URL、不安全Cookie、重复clearance、异常/超长/重复键回复、无效目标不调用、取消持有及排队deadline、一次调用不自动重试、敏感数据不输出。

证据：`.toolchain/browser-adapter-tests-final-20261115.jsonl`、`.toolchain/browser-adapter-linux-race-20261115.jsonl`。Linux为固定本地Go测试镜像内的无网络、只读源码、非root临时容器，命令为`go test -race -p 1 -json ./internal/engine/browserassist -count=1`，正常退出后--rm清理。go vet通过；65项备份契约检查、16份冻结摘要、git diff --check通过。两平台测试范围重叠，不相加作为32项独立用例。

## 未交付与下一步

这不是“FlareSolverr已经能访问真实书源”。未实现网络Gateway、浏览器出网代理/隔离、加密Redis缓存、singleflight、分布式锁、服务全局限额、管理配置入口或真实站点求解。Header生成器也不会控制调用方HTTP客户端之后的跳转；接入时必须逐跳重新校验或移除凭据。

下一步先实现可信管理层的有界网络Gateway及所有浏览器请求的出网约束，并用模拟辅助器验证私网子资源/重定向/DNS重绑定拒绝，再设计缓存和共享任务生命周期。现有搜索10秒预算保持不变，不将协议组件串到线上请求中等待60秒，也不因书源JSON字段启用浏览器。
