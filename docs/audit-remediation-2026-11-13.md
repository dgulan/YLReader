# 审计整改：第一批（2026-11-13）

基于用户审计，优先处理错误就绪、存量结构保护、配置门禁和连接串兼容。研究、schema.sql、architecture.md、openapi.yaml 保持 Frozen v1.5。

## 处置结果

| 审计项 | 本批处置 | 剩余范围 |
|---|---|---|
| P1-1 Redis 就绪假阳性 / 配置双轨 | 缓存与探针共享客户端；REDIS_URL 统一入口、ADDR 兼容；地址冲突拒绝启动；未配置省略 redis；必须收到 PONG；失败使用契约值 fail/503 | 未配置 Redis 仍允许开发/单节点内存缓存 |
| P1-2 存量库缺列即 500 | 启动前只读视图检查；缺列拒绝启动；运行中 checks.schema=fail/503 | **部分缓解**：没有 schema_migrations、完整版本校验、生产迁移 job 或自动 DDL；仍须备份及审查迁移 |
| P1-3 Compose 配置门禁 | 新增空值 .env.example、忽略真实 .env、公开仅校验用 dummy 文件；COMPOSE_ENV_FILE 可指定校验配置，quiet 不打印密钥；CI 加入校验 | 缺真实密钥的部署仍故意失败，不得用 dummy 启动实例 |
| P1-4 kv conninfo | 门禁支持受限 libpq keyword/value、单引号和反斜杠转义，保留原目标限制 | 不支持完整 libpq 参数；生产库授权边界不变 |
| P1-5 计时 / 大书预算 | 本机全量集成串行通过；Makefile 全量集成及 race CI 使用 -p 1 -parallel 1，保留测试内部并发，未调阈值 | **未闭环**：并行负载敏感性、90s 导入预算、慢盘大书、异步/续传、CI 首跑仍待专项 |
| P2 回填拖垮正文 / 重复查询 | 回填失败只记录日志并返回正文，回填事务最多 500ms；正文复用鉴权取得的 book，本地正文不额外取进度 | 回填失败时位置继续待恢复，不伪造保存成功 |
| P2 文档与工具链口径 | 标示历史数字，更新当前入口；race/frontend/容器统一 Go 1.24 | 未声称远端 CI 已运行 |

## 实现边界

Redis 原实现还将 NOAUTH 等任意回复当作 PING 成功，并在错误时返回契约外 error。本批一并修正。URL 中认证、数据库编号和 TLS 用于探针及实际缓存连接；只配置 REDIS_URL 也会启用真实 Redis 缓存。

结构检查是 `SELECT id, progress_char_offset, progress_version FROM v_bookshelf LIMIT 0`，不读用户行或执行 DDL，且在种子用户、Worker 和 HTTP 监听前运行。标识 bookshelf_progress_v1_5 只表示本次已知视图要求，不是完整数据库版本。Compose 改探测 readyz，healthz 只表示进程存活。

kv 门禁允许含 !@、空格、正确转义引号的密码；拒绝 hostaddr/service/options、重复键、嵌套 URI dbname、多主机、非白名单库及目标相关 PG 环境覆盖。状态输出不包含连接串或密码。

正文回填保留书籍锁、活跃源复查和版本条件。快照及 Begin/Lock/Get/Exists/Update/Commit 失败均可降级；读权限和正文抓取错误不被吞掉。

## 验证

- 普通 Go 全包测试、integration vet 通过。
- 静态门禁 61 项通过；kv 连接串在本轮拥有的 55439 隔离集群实测门禁及幂等 schema 应用。未操作原 5432 实例或用户审计使用的既有 yreader_test。
- 新增单测覆盖可选依赖、失败依赖、schema 失败、错误 PING 回复、AUTH/SELECT 同时用于 PING/GET/SETEX、配置冲突。专项集成覆盖旧视图拒绝、回填写失败正文仍可读；原换源与并发保护通过。
- 全量原生集成 `go test -tags integration -p 1 -parallel 1 -json ./... -count=1`：**182 个顶层测试通过，含子测试 267 个通过事件，0 失败、0 跳过**。日志 .toolchain/audit-integration-20261113.jsonl；未改原阈值，同名导入 93.11ms，3000 章 TXT 11.478s。串行结果不等于并行压测结论。
- 新生产后端构建通过。独立容器编排仅配 REDIS_URL，启动与就绪正常；额外进程验证不配 Redis 时 200 且省略该项，URL/ADDR 冲突拒绝启动。
- 在本轮拥有的临时容器库中可逆更名视图列，验证现进程就绪 503/schema=fail、新进程拒绝启动；恢复列名后就绪 200。
- 实际停止本轮专属 Redis 后就绪返回 503/redis=fail，恢复后返回 200；未停止其他 Redis。
- 修复后的后端配合生产 Nginx，8 项浏览器测试全部通过（36.2s）；前端类型检查、38 项单测通过。首轮书签测试曾将上一章“已同步”状态当成新章保存完成，已增加目标章进度 PUT 成功等待再刷新，不放宽断言。
- dummy Compose 配置校验通过。未安装 make，实际验证为等价命令及 Makefile 静态测试，不声称调用过 make。

## 后续

优先完成受审查的版本化迁移设计与 CI，再处理导入预算和性能门禁。多标签页 refresh 轮换、sessionStorage 风险、强幂等、取消后迟到终态写入的观测及发布项继续保留。

未提交、推送、远端 CI 或业务发布；未增加契约字段、删除 chapter_cache 或自动迁移真实持久库。10 个冻结文件 SHA-256 保持一致。

验收结束已删除本轮 yreader-audit-20261113 的 4 个服务容器、专属网络及唯一测试卷；额外探针实例已移除，55439 验证集群已停止。日志与验证镜像保留；既有业务服务未停止。
