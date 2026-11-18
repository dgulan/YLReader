# worker 最终镜像验收范围

继承 ADR-021，不改公开 API、DDL 或备份契约。仅构建本地镜像、运行独立合成验证和扫描，不部署或发布镜像。

最终 scratch 镜像必须包含静态 worker 和内嵌 IANA 时区库，使显式 TZ（例如 Asia/Shanghai）下的 JavaScript Date 与 java.timeFormat 行为与父服务一致。API 与 worker 必须配置相同 TZ；不挂载宿主时区文件。未设置 TZ 时使用 UTC，不继承宿主配置。

分别构建 linux/amd64、linux/arm64，用最终镜像而非外部二进制运行既有 IPC/取消/OOM/重启回归，记录原生或模拟执行属性。镜像检查非 root、入口、架构和内容；扫描所有严重级别，不忽略未修复漏洞。扫描数据库时间及失败必须如实记录，不用旧扫描冒充本轮结果。CI 增加 worker 双架构构建与扫描矩阵。
