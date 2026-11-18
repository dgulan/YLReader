# 独立沙箱 worker 开发与验证（2026-11-14）

对应 sandbox-worker-design-2026-11-14.md、sandbox-worker-operations-2026-11-14.md 和 architecture ADR-021。本轮完成显式启用的独立 worker 接入；默认仍是进程内模式。未部署、提交、推送或合并，未读取 references，未触碰持久业务数据。保留此前未提交工作。

## 交付行为

新增 cmd/sandboxworker、Linux 隔离配置检查、私有 Unix socket 和有界双向 IPC。worker 无出网权限、数据库或生产凭据；HTTP 由父进程原有 Fetcher 代理，沿用原始可信 context，继续执行 SSRF 与域名/书源准入。每次执行新建 Runtime，仅共享有界的不可变 Program 缓存。

显式配置 YREADER_SANDBOX_SOCKETS 后，API 启动握手与 readyz 检查 worker；不可用时失败，不回退本进程。协议拒绝超长帧、错误序号、重复/未知字段、大小写变体、错误类型、尾随数据和超额 HTTP 请求。传输保留整数及负零语义，输入先复制为纯数据，避免调用自定义 MarshalJSON。

父连接断开会取消正在代理的请求；单次调用不重试，不重放 POST。worker 中无法正常结束的执行由看门狗结束进程；OOM 由受限 cgroup 处理，容器重启由外部部署系统负责，后续新调用重连。API 不获取 Docker socket，也不启动容器。

## 实测证据

最新真实容器验证为 `.toolchain/sandbox-worker-container-b84ced91380b/result.json`、同目录 `tests.jsonl` 与 `oom-events.jsonl`。执行仓库脚本 `python scripts/verify-sandbox-worker.py --image yreader-pre-release:race`，使用已存在的固定镜像 `sha256:6ab45148ce1526ce135f5ee64b346c8f7c14a11e48a450c70c1ebb6844a2dba9`，不拉取镜像。

Linux 静态 worker 二进制 SHA-256：`9df6b81d2d99a78b68be69b0c2fb095d60bc282bcdbe9b64a8101275dcd2ab7f`。配置为非 root、只读根、五组 capabilities 全零、no-new-privileges、seccomp、无网络、一核、128MiB 内存且无额外 swap、pids=64、on-failure:3。另启动根文件系统可写的负例，worker 退出1，未进入服务。

两个顶层容器集成测试、七个测试通过事件（含子测试），0失败、0跳过。覆盖普通/结构化返回、原始源身份、真实 Fetcher SSRF 拒绝及源准入、取消、64次HTTP上限、解析器调用/数值语义。512MiB 原生分配仅在 worker 容器执行：记录一次 OOM 事件、一次重启；父测试进程正常退出，之前 POST 恰好执行一次，后续新调用恢复。四个临时容器及一个专用 IPC 卷均按随机所有权标签清理。

50轮超时分类 Linux race 定向回归通过，记录 `.toolchain/sandbox-worker-timeout-456f9ec9a4cf/`。完整回归首轮发现 socket 截止时间已到而 context 定时器尚未调度时误报普通执行失败；修复为同时检查绝对截止时间，不放宽超时。更早的四个单测失败来自夹具目录0755不符合私有IPC的0700契约，已修正夹具，生产权限检查保持不变；失败记录保留。

最终全量 Linux 后端 `go test -race -p 1 ./... -count=1` 通过，记录 `.toolchain/sandbox-worker-race-598ba8982d3f/`：196项顶层、含子测试449个通过事件，0失败。5个顶层条件跳过为两个子进程helper及三个需要真实数据库的测试；另有3个无测试包。黄金解析保持40/44（90.9%）、6个显式不支持，未降低门槛。Linux全包带integration标签的 `go vet` 通过，记录 `.toolchain/sandbox-worker-vet-fbc84d58fa3a/`。这些运行使用最新代码，所建临时容器全部清理。

最终真实 PostgreSQL16/Redis7 的 API、Service、Repository、Migration 集成 race 回归通过，记录 `.toolchain/sandbox-worker-db-81a332859db2/`：147项顶层、含子测试170个通过事件，0失败、0跳过。数据库只存在于自有无外部端口临时容器，三个临时容器均已清理；覆盖上述三个普通回归中的数据库条件跳过项。该范围与普通回归重叠，不能相加。

前端 `npm run generate:api`、`npm run type-check` 通过；后端通过固定v2.4.1生成器（本地模块缓存代理）执行 `go generate ./internal/api`。备份65项契约检查与16份独立冻结摘要匹配，主冻结表10份摘要匹配，`git diff --check`通过。

## 验证边界

各轮测试范围重叠，不相加作为独立覆盖数。前端本轮只有契约说明的生成文件同步，未重跑浏览器验收。OpenAPI仅更新readyz说明，43个操作、响应结构/错误码、SQL DDL和数据库版本不变；前后端生成文件均经固定生成器同步。

backend/Dockerfile.sandboxworker 已提供，但本轮没有完成该最终镜像的双架构构建、安全扫描或部署。128MiB实验不保证接受所有32MiB逻辑输入；上限是拒绝阈值，不是内存峰值承诺。生产编排、时区兼容、真实书源/长正文/最大树、跨架构资源成本、p50/p95/吞吐及探测容量仍需独立验收。默认进程内模式仍不能提供逐次执行的操作系统内存硬上限。
