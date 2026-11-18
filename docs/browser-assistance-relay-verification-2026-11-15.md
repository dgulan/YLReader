# TCP→Unix通道与隔离拓扑验收

依据browser-assistance-relay-design-2026-11-15.md，新增Linux LoopbackRelay与两容器模拟验收脚本。未部署生产监听器/浏览器/FlareSolverr，未接入Fetcher/API/Compose，未提交、推送、合并。没有访问真实书源、已有书库或业务数据库。

## 通道能力

Relay仅接受loopback TCP监听，目标Unix路径由管理代码固定；构造及每次拨号检查绝对路径、无symlink组件、当前UID拥有的0700叶目录和0600 socket。非Linux明确拒绝。默认4连接、30秒、每方向16MiB，不排队，达到容量直接关闭新连接。取消关闭监听和两端连接，所有复制任务退出后才释放名额并返回。

源实现没有公开“任意主机/端口/Unix路径转发”API，也没有自动创建生产监听；实际承载/控制通道需要后续独立配置。Unix检查不防同UID恶意进程替换私有目录内容，必须按租户/任务隔离IPC挂载，不能共享所有用户的可写socket目录。

## 双容器实验

固定已存在的Go测试镜像ID `sha256:6ab45148ce1526ce135f5ee64b346c8f7c14a11e48a450c70c1ebb6844a2dba9`。两个容器均network none、无宿主端口、非root10001、只读根、cap-drop ALL、no-new-privileges、256MiB/无额外swap、1CPU/pids64，只共享本轮随机标签的IPC卷和只读测试二进制。初始化容器仅设置专属卷属主/权限。

代理容器运行真实EgressProxy与本地TLS站点，客户端运行真实LoopbackRelay、HTTP代理客户端和证书验证。测试专属DNS/拨号钩子把模拟公开地址8.8.8.8:443固定映射到代理容器的TLS夹具；这不是生产SSRF例外，也没有真实公网请求。证书只在夹具间共享，不使用InsecureSkipVerify。

最终二进制SHA256 `926724ac01ad4751eca1d4b90f4a53f98aa11e33dca789f40f8ebfc76cb41284`，连续两次串行通过：

- `.toolchain/browser-relay-topology-49eebf24b839/result.json`
- `.toolchain/browser-relay-topology-c1c9347b662c/result.json`

两次均proxy_exit=0、client_exit=0，两个容器network=none，初始化/客户端/代理容器与IPC卷清理全部true。每次验证：

1. 客户端仅loopback；直接访问代理容器TLS站点的loopback地址失败，直接IPv4/IPv6 TCP、UDP DNS与UDP443失败。
2. 本地TCP→Unix→CONNECT→TLS能读取topology-ok，SNI与证书匹配。
3. 未授权域名、私网目标不能经代理访问。
4. 保留一个未发送ClientHello的隧道，再通过测试专属stop标记取消代理；该连接在测试读超时之前断开，新请求失败，代理与relay名额均回收。

stop文件是夹具的可信测试控制机制，不是生产停止API；夹具客户端IPC可写以发此标记，不能把它当成已验收的真实浏览器只读挂载/授权控制边界。浏览器侧只读IPC挂载与独立反向控制通道仍待实现。

## 回归与失败记录

最终Linux integration race：24项顶层通过、含子测试40个通过事件，0失败；一个拓扑helper按设计跳过，两个角色已在上述容器分别实跑。Windows：21项顶层、37个通过事件，0失败/跳过；Linux专用测试未在Windows运行。范围重叠，不累加为独立覆盖数。

日志 `.toolchain/browser-relay-linux-final-20261115.jsonl`、`browser-relay-windows-20261115.jsonl`；go vet通过。新增验证Unix权限/symlink/普通文件拒绝、非loopback监听拒绝、连接容量拒绝与取消后回收。

早期拓扑ce394f92019a通过。加强取消断言后的3d94d2eeda55与并行race执行时，首个HTTPS请求在3秒夹具预算中超时，尚未执行到取消检查；client_exit=1、proxy_exit=0，全部临时对象已清理。这是保留的失败，不能计为成功或仅凭它断言生产缺陷。相同二进制/安全断言/资源和时间限制在无并行负载下连续两次通过，但未做长时稳定性或证明超时根因，不承诺性能。

## 剩余门禁与复现

本轮证明Go夹具的模拟通道与网络命名空间边界，不是Chrome/FlareSolverr已经完全接入。下一步为只读IPC与受限反向控制路径、真实solver配置/子资源/取消后资源清理，再评估外部代理网络与真实站点。现有搜索/默认模式不变，Safari/ARM/真实阅读等既有门禁继续保留。

在backend用Go1.26.11、GOOS=linux、GOARCH=amd64、CGO_ENABLED=0：

```text
go test -c -tags integration -o ../.toolchain/browser-relay-tests-linux ./internal/engine/browserassist
```

回到项目根运行：

```text
python scripts/verify-browser-relay-topology.py --image yreader-pre-release:race --binary .toolchain/browser-relay-tests-linux
```

脚本不拉取镜像，仅清理匹配本轮随机标签的对象；测试结果与镜像/二进制摘要一并留在忽略目录。备份65项契约检查、16份冻结摘要及git diff --check为收尾校验；没有声称全量后端/浏览器/正式镜像扫描重跑。
