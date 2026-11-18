# worker 镜像构建与安全验收

对应 sandbox-worker-image-design-2026-11-14.md，继承 ADR-021。未部署、发布镜像、提交、推送或合并；未触碰持久业务数据。此记录不表示所有发布门禁已通过。

## 变更

worker CLI 内嵌 Go IANA 时区库，scratch 无需挂载宿主时区文件即可遵守 TZ。测试核对 JavaScript Date 的时区偏移与 java.timeFormat 的当前日期；父服务和 worker 应配置相同 TZ，未显式设置时使用 UTC。

验证脚本新增 --worker-image、--platform，可直接测试最终镜像，不再必须绑定外部二进制；检查镜像架构、默认非root用户、入口和命令，保留原有拒绝配置、IPC、取消、OOM事件、重启及POST不重放门槛。期望时区由 TEST_SANDBOX_WORKER_TZ 传入，未传时按UTC检查。事件范围使用Docker引擎时间，避免依赖宿主与桌面虚拟机时钟一致。

security.yml 增加 worker，形成 backend/frontend/worker × amd64/arm64 六组最终镜像构建及全部严重级别扫描；已用本地js-yaml解析核对矩阵。没有运行远程CI。Dockerfile使用内置解析器，减少额外外部前端镜像依赖，固定Go构建镜像摘要不变。

## 完整多阶段构建

从项目根运行：

```text
docker buildx build --platform linux/amd64 --load -f backend/Dockerfile.sandboxworker -t ylreader-worker-check:amd64-20261114 backend
docker buildx build --platform linux/arm64 --load -f backend/Dockerfile.sandboxworker -t ylreader-worker-check:arm64-20261114 backend
```

两者均成功，日志为 `.toolchain/worker-image-amd64-retry.log`、`worker-image-arm64-retry.log`。镜像ID分别为：

| 架构 | Docker镜像ID | 镜像内worker SHA-256 |
|---|---|---|
| amd64 | c8138b9ad87ee9f48c3b163518a81a5da12f1ec05614302431cb352867b7d78e | 6f723609277dabc5654a06d56582215c62ec8992cb4b9a6b55b77c82c8c6a91d |
| arm64 | 34c9c45a5585fddcfb8800d7b8cb2c191abcead9a20cf46bba6ba06706a61492 | 90854d539afbe1b4a692eb0a79c9b967181486416d90f028354b7a0c1df657de |

导出两份镜像逐层检查，运行层均只有worker，不含工具链、源码、环境配置或参考仓库；用户10001:10001，入口/命令符合协议，无声明端口或额外volume。构建期间曾遇到Docker Hub认证超时，重试后完整构建成功；早期离线组装镜像仅作为补充证据，不替代这里的正式Dockerfile构建。

## 安全扫描

固定 Trivy 0.76.2，以导出镜像tar文件扫描，不向扫描容器开放Docker socket。漏洞库镜像站下载曾超时，后从ghcr.io/aquasecurity/trivy-db:2成功更新；UpdatedAt=2026-11-14T13:05:54Z，DownloadedAt=2026-11-14T14:12:09Z，NextUpdate=2026-11-15T13:05:54Z。扫描复用该新库时禁用网络和自动更新，未忽略未修复漏洞。

双架构退出码均0，UNKNOWN/LOW/MEDIUM/HIGH/CRITICAL均无检出。两份报告均识别worker Go二进制及9个包；scratch无系统发行版包，不能将其表述成Alpine系统包扫描。完整JSON、库元数据、导出tar和日志保存在 `.toolchain/worker-image-security/`，以 `final-amd64`、`final-arm64` 命名。无已知漏洞仅代表该版本扫描器/数据库与识别范围的结果。

## 运行验证与剩余门禁

amd64最终镜像第一轮 `.toolchain/sandbox-worker-container-66542d31df9a/` 通过：两个顶层、含子测试8个通过事件；覆盖时区、IPC、源身份、SSRF/准入、取消、HTTP计数、OOM后新调用恢复及POST只执行一次。一次OOM事件、一次重启，临时容器与IPC卷全部清理。

最后将期望时区改为测试环境参数，并改用引擎时间获取事件后，`.toolchain/sandbox-worker-container-38e4dabc8d30/` 同样通过，OOM事件和重启各一次。中间一次 `.toolchain/sandbox-worker-container-5d42dea43849/` 的父测试退出0、重启一次，但未收集到OOM事件，按原门槛保留为失败记录，不作为成功证据。未为通过测试降低任何隔离或OOM判断条件。

本轮未修改业务API/DDL或冻结文档；备份65项契约检查、16份独立摘要及主冻结表10份摘要匹配，git diff --check通过。生产改动仅worker CLI内嵌时区数据；没有重复计入上一轮全量后端/数据库回归数量。

arm64在本机x86 Docker Desktop模拟运行未通过，记录 `.toolchain/sandbox-worker-container-7d18cd164782/`。worker在启动隔离检查时失败，没有提供服务，未回退或放宽门槛。独立只读诊断 `.toolchain/worker-profile-arm64.log` 显示：UID10001、PID1、单进程cgroup、内存/swap/CPU/pids与capabilities符合约束，但statfs根Flags返回0，与要求的只读标志不符。该观察不能证明真实ARM主机有同样行为，也不能单凭模拟失败认定仅为模拟器问题；必须在真实arm64 Linux/cgroup v2主机复核后才能关闭该运行门禁。

因此本阶段为“双架构构建及扫描完成，amd64运行验证完成，arm64原生运行待验收”。性能、长正文/最大输入资源峰值、真实来源兼容性、探测容量、生产编排及发布批准仍分别验收。
