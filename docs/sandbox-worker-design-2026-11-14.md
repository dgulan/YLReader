# 独立沙箱 worker 与父进程代理 v1

日期 2026-11-14；实现前冻结的内部协议。继承 ADR-020 数据预算与每次独立 Runtime，不改研究、公开 API/DDL、数据库版本或备份格式，不部署、提交或推送。

## 进程与信任边界

选择预先由部署系统启动的独立 Linux worker 容器；API 通过私有 Unix socket 调用，不持 Docker socket，不在 API 内启动特权容器。每个 worker 同时处理一份执行，成功后可处理下一份，但 Runtime 每次新建。父进程连接池有界，默认最多四个并发，排队 250ms，总预算 3 秒。连接从握手、输入到 HTTP 代理及最终结果均服从同一 context。

worker 启动检查专有 cgroup v2 namespace、非 root、只读根文件系统、NoNewPrivs、五组 capabilities 均为零、seccomp、仅 loopback 接口、单进程 cgroup、memory.max 有限且不超过 256MiB、memory.swap.max=0、pids.max<=64，以及有限且不超过两核的 cpu.max 配额。能力缺失即退出，不静默放宽。IPC 目录必须为当前 UID 拥有的绝对私有目录（0700，无 symlink 路径组件），socket 为0600；目录锁确保仅清除本 worker 先前崩溃遗留的 socket，不删除其他文件。每个 worker 只挂载自身 IPC 目录；父进程可挂多个目录。

worker 不接收服务环境、DB/Redis/Storage/认证配置，也不持源身份。父进程始终用原始 Go context 调用现有 HTTPClient，因此书源 UUID、双层准入、SSRF、响应限制和取消机制继续生效。子进程不能指定可信用户、源 ID、Header 或代理目的之外的服务配置。镜像必须是可信、无生产凭据的固定本地 image ID；无网络、只读、cap-drop、no-new-privileges、CPU/内存/pids 限制由部署系统明确提供。

## 有界协议

每个连接只执行一次：worker Hello → parent Eval → 零至64次 HTTP/Reply → worker Result → EOF。13 字节帧头为 magic `YLW1`（4）、kind（1）、大端 seq（4）、大端 payload length（4）。握手和初始请求 seq=0，HTTP 从1递增，最终 Result 为下一序号。错误 kind/序号/长度在分配 payload 前拒绝；不支持流水线或并发 HTTP。

JSON payload 必须是对象，字段大小写精确、无重复、无未知字段、无尾随内容；递归结构及 token 数量有界。各帧最大值：Hello 1KiB、Eval 40MiB、HTTP 请求2MiB、HTTP Reply 24MiB、Result 8MiB。Eval 包含 script、vars、structured；父进程先验证脚本256KiB/输入32MiB预算，并复制为无 Go 方法的 JSON 数据，避免 MarshalJSON 或共享可变输入进入通信边界。

HTTP 仅 GET/POST，URL<=16KiB、请求正文<=1MiB、响应正文<=16MiB；正文以 base64 JSON 表示。每次请求计数包括失败请求。父进程不重放、重定向或重试整个 worker 执行；Fetcher 自身已有受控重试仍按原契约执行。proxy 不回传 Go error 文本或凭据，只回固定错误类别。Result 必须是成功值或既有 sandbox 错误二者之一，不能提供新的公开错误码；结构化结果重新按 JSON 数据预算检查。

## 取消、崩溃和恢复

双方使用独立读循环监测对端 EOF：worker 崩溃时立即取消父进程正在进行的 HTTP；父进程断连时取消 worker。父进程的 HTTPClient 必须遵守 context，不为无法取消的原生调用启动遗留 goroutine或提前释放名额。I/O 截止及主动关闭解锁阻塞读写；所有读循环退出后才归还池名额。

worker 的原生 JS/Go 内建可能不受 Interrupt 控制：取消或截止后250ms仍未返回时，由 worker 自身 watchdog 以退出码124终止进程；OOM由独立 cgroup 限制。部署系统负责有界重启，父进程仅使本次调用失败，不自动重试；后续新调用连接重新启动的 worker。崩溃发生在 POST 后时无法证明远端回滚，故绝不盲重放。

## 接入与验收

新增 cmd/sandboxworker 和 sandbox.NewRemote；`YREADER_SANDBOX_SOCKETS` 非空时服务器显式启用并逐端点握手/探测，缺失能力、不可连接或协议不符则启动失败。变量为空保持现有模式；配置了 worker 后绝不自动回退。该接入是代码能力，不是本轮部署或完成正式发布。

必须测试：帧/JSON/长度/重复/乱序/错误组合拒绝；JSON 所有权及类型边界；HTTP数量与方法限制；进程/连接取消；EOF期间阻塞HTTP取消；CPU/转换超时；多次调用状态隔离；大分配只杀 worker、后续新调用恢复。真实隔离容器与父进程分别运行，保留 OOM/restart/cleanup 证据；不在主服务测试进程执行无界分配。兼容性、p95/吞吐和部署总预算分开记录，不把 IPC 通畅等同于全部验收完成。
