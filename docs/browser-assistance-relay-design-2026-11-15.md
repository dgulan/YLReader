# 本地TCP→Unix代理通道与隔离夹具

新增Linux通道组件，只接受loopback TCP listener，目标为管理层指定的绝对Unix socket；每次拨号检查无symlink路径、当前UID拥有的0700叶目录及0600 socket。客户端不能选择Unix路径。默认4个连接、30秒、每方向16MiB；取消后关闭监听/连接，等待所有复制任务退出再返回。无配置入口、无生产监听或部署。

隔离验收使用两个独立--network none容器及唯一0700 IPC卷：代理端运行真实EgressProxy和本地TLS模拟站点，客户端仅能通过本地relay→Unix socket→CONNECT→TLS访问。测试专属DNS/拨号钩子把模拟公开IP映射到代理容器内TLS夹具；生产构造器没有该钩子配置。证书仅在内存生成，公开CA写IPC，客户端仍校验证书；不使用InsecureSkipVerify。

客户端验证经代理成功、跨域/私网目标被拒、直接IPv4/IPv6/UDP失败、代理端模拟服务的loopback端口不可直连，以及停止代理后请求失败。Unix权限/路径、非loopback监听、取消和连接回收另行测试。两个容器均无外网，证明模拟拓扑与通道，而非真实FlareSolverr或公网访问已通过。

真实浏览器本机HTTP控制口仍需要独立、受限的反向控制通道；不得把本通道公开为任意端口转发器。实际浏览器/solver参数、外部代理出网、全生命周期、租户IPC隔离和真实子资源仍待独立验收。本轮不修改公开API、DDL、默认抓取或Compose，不提交、推送或部署。
