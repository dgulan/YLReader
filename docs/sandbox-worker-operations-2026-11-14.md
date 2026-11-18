# 独立沙箱 worker 配置与复现

这是未部署的显式启用能力，契约见 sandbox-worker-design-2026-11-14.md。默认仍为进程内模式；本记录不是生产发布批准。

后续最终镜像的双架构构建、安全扫描和arm64运行限制见 [镜像验收记录](sandbox-worker-image-verification-2026-11-14.md)。API与worker必须显式配置相同TZ；worker已内嵌时区库。

amd64合成性能和大输入边界见 [性能验收记录](sandbox-worker-performance-verification-2026-11-14.md)。128MiB不是父进程或整组内存预算；大输入在父进程有编码开销，不能直接据worker限额推算部署内存。

2026-11-14 第二轮实测给出了更具体的配套关系：默认 64MiB 父进程在途字节预算下，两个 12MiB 并发调用使 worker cgroup 峰值约 80–84MiB、父容器峰值 141–153MiB；31MiB 逻辑输入在 128MiB worker 下仍会 OOM 并触发一次重启（重启前存活 worker 峰值 116–119MiB）。因此 128MiB 只是实验规格；生产按 <=256MiB 配置 worker，同时为父进程预留编码峰值。`InflightBytes` 由 sandbox Config 控制，默认 2×InputLimit（当前 64MiB），不得低于该下限；预算饱和按既有 504 `sandbox_timeout` 拒绝，不新增公开错误码。

## 启用要求

每个 worker 由部署系统启动为独立 Linux/cgroup v2 容器，直接运行 CGO-disabled 的 cmd/sandboxworker 二进制，不经 shell/init 派生额外进程。必须是非 root、只读根、五组 capabilities 均清零、no-new-privileges、seccomp、network none，有限 pids<=64，memory<=256MiB、无额外 swap、CPU配额不超过两核，并配置有界 on-failure 重启。实验使用 128MiB、一核、on-failure:3；128MiB 并未证明能容纳所有32MiB逻辑输入。镜像不得包含生产凭据或用户数据。

每个 worker 只挂自己的 IPC 目录（UID 与 API 一致，当前容器用户10001，目录0700）；worker 命令为 `/worker -socket /ipc/worker.sock`。该目录可写以创建0600 socket和锁，除此以外不挂数据库、书库、配置、宿主根目录或 Docker socket。worker 启动检查不满足时直接失败。

API 需要分别挂载各 IPC 目录，并设置 `YREADER_SANDBOX_SOCKETS=/run/worker-1/worker.sock,/run/worker-2/worker.sock`。这里是容器内绝对路径，不能只设置宿主路径而不挂载。Compose 默认只透传空值，没有自动创建 worker 或 IPC 挂载；实际生产编排需要单独审阅，不能对已有环境直接套用测试脚本。

启动时每个端点必须完成协议握手；配置错误或 worker 缺失则 API 启动失败，不回退本进程执行。readyz 在配置该依赖时增加已有 checks map 的 sandbox 项；不可用返回503。繁忙 worker 的串行握手也可能使就绪探测失败，它不等于进程死亡；生产探测间隔/容量和总体资源预算仍需验收。

父进程不启动容器、不接触 Docker daemon。worker 后续不可用只使当前执行失败；不会重放整个 JS 或其中的 POST。部署系统重启后，后续新执行重新连接，不保留旧连接/Runtime。父进程HTTPClient必须遵守context，否则仍不能安全提前释放名额。

## 本地隔离验证

先在 backend 模块目录设置 GOOS=linux、CGO_ENABLED=0，执行 `go build -buildvcs=false -o ../.toolchain/sandboxworker-linux ./cmd/sandboxworker`。再回到项目根目录运行 `python scripts/verify-sandbox-worker.py --image <可信且已存在的Go测试镜像> --binary .toolchain/sandboxworker-linux`；脚本不拉取镜像，不接入生产服务。

测试最终镜像时改用 `--worker-image <已构建镜像> --platform linux/amd64`（或linux/arm64），不得同时指定--binary。脚本检查镜像默认配置，以TZ=Asia/Shanghai运行worker并传递期望时区给父测试。arm64模拟执行不替代真实ARM主机验收。

backend/Dockerfile.sandboxworker 提供最终镜像构建定义。初始IPC轮仅验证静态二进制，后续镜像轮完成了双架构构建与漏洞扫描，并验证amd64时区一致性；arm64原生运行、生产编排及其余发布门禁仍未关闭，具体证据以镜像验收记录为准。

脚本创建带随机标签的专用 IPC volume，短生命周期初始化容器仅设置该卷属主/权限，然后启动受限 worker 与独立父测试容器。父容器无网络，HTTP 以合成适配器及真实 Fetcher 的拒绝路径验证，包含原始书源上下文、SSRF、取消、请求上限、OOM 后重启与POST不重放、解析器语义/数值传输。所有临时对象只按本轮随机标签清理；不会移除既有卷或容器。

协议输入上限是拒绝阈值，不是最低接受能力或内存峰值保证。真实来源兼容性、长正文/最大树、跨架构资源开销及p50/p95/吞吐仍独立验收；本轮代码和实验不取消这些门禁。
