# 统一出网域名限速验证（2026-11-14）

对应 [内部契约](domain-admission-design-2026-11-14.md)。本轮交付域名层，未部署、提交、推送或合并；此前未提交修改保留。未修改 DDL、OpenAPI、研究及备份冻结文件，没有访问参考仓库或生产服务。

## 行为与实现

生产服务器在 Redis 配置解析后创建共享 Fetcher，并向搜索、目录、正文、调试和 JS 桥接注入同一实例。域名预算与用户/书源无关：同一规范化 hostname 的 HTTP/HTTPS、端口、大小写、IDNA 和尾点变体共用预算；默认相邻准入至少 500 ms、单次等待最多 5 秒。子域名不自动合并。

Redis Take 使用原子 EVAL/PTTL/SET PX；客户端复用已有认证、TLS 和 DB 配置。Redis 故障不降级，损坏的无 TTL 键不放行；TTL 到期无需清扫。未配置 Redis 时最多保留 4096 个内存域名状态，仅回收过期条目。存储错误不带出服务端回复。

准入在 RoundTripper 层执行，Fetcher 重试和 HTTP 重定向每次重新准入；JS 桥接同样经过该层。等待失败关闭请求体，不继续拨号或自动重试；取消发生在存储返回期间亦拒绝后续出网。现有连接级 SSRF 检查不变。

这不是连接并发上限或逐 TCP 发送限速。Go Transport 内部自动连接重试共用一次预算；Redis 数据丢失可重置预算；内存模式不跨进程。这些边界和缺省策略写入契约，未声称完成书源 concurrentRate 策略。

## 验证证据

新增 10 项顶层回归测试（含 Redis 协议子用例），覆盖域名归一化、64 个内存竞争者仅一个获准、容量拒绝与过期回收、等待和取消、拒绝时不拨号/不重试/关闭请求体、重试与跨域重定向计费、JS GET/POST 桥接、Redis AUTH/SELECT/EVAL 及非法回复拒绝。实际临时 Redis 测试中，24 个独立客户端竞争相同键恰有一个获准，另验证域名隔离、过期重入和无 TTL 损坏键拒绝。

首轮 Linux race 覆盖整个引擎、缓存与服务器：59 项顶层测试、含子测试 141 个通过事件，0 失败；一个 XPath 子进程专用 helper 跳过。黄金样本维持 40/44（90.9%），另有 6 项显式不支持。记录 `.toolchain/domain-admission-76ece703e59d/tests.jsonl` 与 `result.json`，两个临时容器均清理。该轮尚未包含最后新增的 JS 桥接测试及存储返回期间取消检查；后续定向回归单独记录。

最终定向 Linux race 覆盖 Fetcher、缓存与服务器：29 项顶层测试、含子测试共 45 个通过事件，0 失败、0 跳过；JS 桥接、存储返回期间取消及真实 Redis 集成均已执行通过。记录 `.toolchain/domain-admission-e0be97dfc649/tests.jsonl`、`result.json`，Redis 与 Go 临时容器全部清理。两轮数字有范围重叠，不累加为独立测试总数。

`scripts/verify-backup-contract.py` 最终通过 65 项检查、16 份冻结摘要；`git diff --check` 通过。本轮不存在前端、DDL、API 或冻结摘要变更。

全后端 `go test ./... -run '^$'` 编译通过；Fetcher、缓存与服务器 `go vet` 通过，日志分别为 `.toolchain/domain-admission-compile.log`、`.toolchain/domain-admission-vet.log`。编译检查不计作测试执行。Windows 直接执行原有回环服务测试受沙箱连接权限限制而失败，因此运行证据采用隔离 Linux 容器，不将环境失败计作代码通过。

## 后续

后续进展：书源层已继续实现，参见 [组合准入验证](source-admission-verification-2026-11-14.md)；下述待办描述保留本轮域名层交付时的范围，不将旧测试数字当成后续实现的证据。

书源 concurrentRate 的跨请求并发/窗口语义、租约与崩溃回收、与域名预算的原子组合仍需单独设计。真实书源吞吐、排队公平性、Redis 故障转移及多主机演练尚未验证。本轮未修改前端，未重跑浏览器验收。
