# 沙箱资源边界 v1：执行约束与进程隔离实验

日期 2026-11-14。研究、DDL、43 个 API 及备份冻结格式保持不变。本轮开发生产执行约束及独立隔离验证工具，不切换线上执行方式，不部署、提交、推送或合并。

## 已确认的边界问题

原来的成功返回值在 run 监视结束后才执行 String / Export，用户 toString、getter 或 result getter 可在此阶段再次执行 JS；原来的 sync.Pool 仅清理注入键，不能清理新增全局、var/let 声明、原型或桥接对象修改。为避免跨调用污染，每次新建 Runtime，只复用不可变 Program 缓存。旧的变量和 source.setVariable 仍在单次执行内生效，不承诺跨求值共享全局。

## 本轮生产约束

- 每个生产共享 Sandbox 默认最多 4 个并行执行（可配置，上限 64），排队最多 250ms；等待服从外层取消。原有 3 秒脚本预算覆盖排队、输入检查、编译后执行和最终转换。拒绝/取消采用既有 sandbox_timeout，不新增公开错误码。
- 脚本默认最多 256 KiB；输入变量总 JSON 近似预算 32 MiB、根变量最多 1024、深度最多 32、节点最多 65536。仅接受 JSON 数据树，拒绝 Go callback、Runtime 对象及循环图；输入树只验证不复制，调用者不得并发修改。超额/不支持输入使用既有 rule_execution_failed，检查早于编译和 Runtime 注入。
- Eval 仍返回 JS 字符串（默认最多 1 MiB）。执行、result 兜底 getter、字符串转换、结构化导出均在同一个 Interrupt 监视周期内。监视退出并等待 goroutine 完成后才归还并发名额。
- EvalValue 返回独立 Go JSON 数据树；字符串、有限数字、布尔、null、普通对象和数组受总 JSON 编码预算 1 MiB、深度 32、节点 65536 限制。数组先检查 length 再分配；拒绝循环、函数及非 JSON 对象，避免返回捕获 Runtime 的 Go callable。getter 仍按 JS 语义执行，受同一截止时间约束。
- 每次重建白名单 java/source/book，不允许输入变量覆盖这三个桥接全局；book 字段继续通过 bookName/bookAuthor/bookUrl 注入。JS 在本次执行内可修改自身环境，下次执行完全隔离。
- Program 缓存的字节权重仍是源码长度，不是编译 AST 的精确占用。编译在短缓存锁外进行，允许有上限的重复编译，避免持锁编译阻塞所有缓存命中者。编译不是可 Interrupt 的 JS 执行；不启动超时后遗留的编译 goroutine，返回前检查取消，名额始终持有到工作实际结束。

这些措施不是每个 goja Runtime 的堆硬配额。单次 JS 原生大分配、原生 Go 桥接/编译、Keys 或字符串转换内部仍可能分配大量内存或不能即时中断；java.put/source.setVariable 的内部 Export 也不能据此声称已具有逐字节硬配额。容器级主服务总内存限制不能证明单个恶意执行不会拖垮 API。

排队时间上限也不是 HTTP 总并发/排队人数上限。输入校验不克隆调用者的 Go 数据；不得跨并发执行共享可变输入，JS 可能修改当前执行的输入 Go 映射。当前服务按请求/引擎创建输入；正式 worker 的序列化边界还需独立解决所有权问题。

## 隔离实验工具

新增独立 cmd/sandboxprobe：stdin 接收一份有长度上限的 JSON 请求，stdout 只输出结果/固定错误类别；不注入 HTTPClient，无数据库、Redis、存储、认证配置，不接入 Web API。验证脚本在无网络、只读、非 root、无 capabilities、禁止提权且限制进程数的临时容器运行编译后二进制，设置 memory.max 对应的容器内存限制并关闭额外 swap。

分别验证正常执行、CPU 超时、资源输入拒绝、JSON 导出和原生大分配。大分配仅在受 OS 限制的独立进程内执行；检查 OOMKilled/退出状态，随后新进程必须能正常执行。该实验是故障隔离证据，不是 API 已使用独立 worker 的证据，不在主服务中调用 debug.SetMemoryLimit 冒充每用户配额。

可复现脚本为 scripts/verify-sandbox-isolation.py。先以 GOOS=linux、CGO_ENABLED=0 构建 cmd/sandboxprobe 到 .toolchain/sandboxprobe-linux（仅实验构建可用 -buildvcs=false）；再运行 `python scripts/verify-sandbox-isolation.py --image <可信的本地镜像>`。脚本不拉取镜像，先解析固定 image ID，核验隔离参数，并仅按本轮随机标签清理自有容器。结果与二进制摘要写入 .toolchain/sandbox-isolation-随机串/result.json。

## 后续正式 worker 门禁

生产 worker 需单独进程/容器及有界双向 IPC；父进程保管 DB、密钥、存储与真实 Source context，子进程不能提供可信租户或源身份。java HTTP 只能由父进程复用现有 Fetcher 执行，继续 SSRF、双层准入、取消和响应上限。还需完成输入序列化兼容性、IPC 各帧限制、父子取消、worker 崩溃/强杀、重建、实际 p95/吞吐及部署资源预算，才能切换生产，不自动 fallback 至无隔离模式。

参考：[goja Interrupt](https://pkg.go.dev/github.com/dop251/goja#Runtime.Interrupt)、[Go 软内存限制](https://pkg.go.dev/runtime/debug#SetMemoryLimit)、[cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)。实现以本仓固定的 goja 版本和 Go 1.26.11 源码为准，未升级依赖。
