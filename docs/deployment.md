# 开发中部署说明

当前前后端已具备首个阅读闭环，设计契约 Frozen v1.5；完整功能和发布验收尚未完成。最新功能及测试范围见 [frontend-progress.md](frontend-progress.md)。下文历史验证记录以最新进展为准。

## 新实例

复制根目录 `.env.example` 为 `.env`，填写 `JWT_SECRET`（随机长密钥）、`YREADER_HEADER_KEY_primary`（32 个随机字节的 Base64）、`SEED_USERNAME` 和 `SEED_PASSWORD`；也可由终端环境提供。示例中的空值不能用于部署。`.env` 已加入忽略规则，密钥需要持续保存，重启时沿用；不要提交到仓库。

执行 `docker compose config --quiet`，然后 `docker compose up --build -d`。PostgreSQL **仅在空数据卷首次启动时**执行 `docs/schema.sql`；应用在数据库就绪且用户表为空时创建初始管理员。已有用户时不会新增种子用户或覆盖密码。

访问 `http://localhost:5173`，前端通过 Nginx 将 `/api/` 代理到后端。前端路由刷新回退到 `index.html`。后端端口 8080 保留供开发调试；该编排为本地部署，公网部署需另配 TLS 与访问限制。

正文缓存和健康检查共享 `REDIS_URL=redis://redis:6379/0` 配置与客户端，支持用户/密码、数据库编号和 `rediss://` TLS。旧 `REDIS_ADDR=host:port` 作为无认证连接兼容入口保留；同时提供时必须指向同一地址，否则启动失败。都不配置时使用内存缓存，`/readyz` 省略 Redis，不伪造 `ok`。配置后认证失败、非 PONG 响应和连接失败均使就绪返回 503。文件卷由镜像中的 UID 10001 写入。

仅校验 Compose、不提供部署密钥时，执行 `docker compose --env-file scripts/compose-check.env config --quiet`，或 `make COMPOSE_ENV_FILE=scripts/compose-check.env compose-config`。该文件中的值为公开 dummy，只能用于校验，不得用它启动实例。普通 `make compose-config` 使用 `.env`，只验证、不输出展开后的密钥。

## 已有实例

2026-11-13 ADR-017 更新：现在启动和 readyz 同时要求版本 1 账本、校验和及视图形状。旧库应先备份并迁移；不要手工填写账本来绕过检查。测试/隔离库使用 `make migrate` 或 `go run ./cmd/migrator schema up`；具体目标限制及验收见 [migration-verification.md](migration-verification.md)。下文“没有版本表”的说明为审计第一批历史状态，已由本修订替代，生产自动迁移仍未提供。

已有 PostgreSQL 卷不会自动重跑初始化 SQL。先备份，再按 Makefile 中的数据库目标门禁执行迁移；本次修改不会连接或改动现有数据库。不得通过删除数据卷来代替迁移。

已有文件卷或绑定目录需要允许 UID 10001 写入；镜像创建目录的所有权只解决新卷初始化，不会自动调整已有卷权限。

`v_bookshelf` 需要 `progress_char_offset`、`progress_version` 两列。应用在创建种子用户、启动 Worker 和监听 HTTP 前执行只读结构检查；缺少表/列则拒绝启动并提示迁移，避免书架运行后出现缺列 500。运行中的 `/readyz` 通过 `checks.schema=ok/fail` 复查该结构要求；Compose 健康检查改用 `/readyz`，`/healthz` 只代表进程存活。

这仍不是完整版本迁移框架：没有新增 schema_migrations 表、自动升级或自动 DDL；结构标识 `bookshelf_progress_v1_5` 只是本次启动要求，不等于完整数据库版本。已有库仍须备份、审查更新后的 schema，再通过独立授权的部署迁移步骤应用。当前 `make migrate` 门禁仅允许本机测试和隔离库，不能当成生产升级入口。

测试迁移支持 URI 和受限 libpq keyword/value 两种 `DATABASE_URL`，例如 `host=127.0.0.1 port=55439 user=verifier dbname=yreader_a1_iso_verify sslmode=disable`。必须显式提供回环 host、端口和白名单数据库；带空格的密码按 libpq 单引号/反斜杠规则转义。拒绝重复键、service、hostaddr、options、多主机和目标相关 PG 环境覆盖，不打印连接串或密码。`make migrate` 内部仍先执行静态门禁，允许后才调用 psql。

## 本地导入预算（ADR-018）

`LOCAL_IMPORT_TIMEOUT` 默认 `90s`，支持 `1s` 至 `10m`（如慢盘设置 `3m`）；非法值在连接数据库前拒绝启动。预算只覆盖接收文件和格式识别完成后的解析/落库阶段，依赖底层协作取消。Nginx `proxy_read_timeout` 为 `660s`；其他反代也需覆盖自己的处理预算。增加预算会延长请求占用时间，不等于异步导入或性能提升。

处理预算超时返回 `504/import_timeout` 与 `Retry-After: 10`；结果可能尚未确认，先刷新书架再决定是否重新选择同一文件，不自动重传。上传上限仍为 100 MiB。断点续传和异步导入尚未提供。

## 退出

SIGINT/SIGTERM 触发 HTTP 排空（最多 30 秒），随后等待 Worker 退出（最多 30 秒），最后释放数据库连接。Compose 预留 65 秒退出时间。

## 历史验证与后续门禁

2026-11-12：普通后端测试、go vet、Compose 配置校验通过；新增服务取消及端口监听失败回归测试。Docker Desktop Linux Engine 当前未运行，尚未验证镜像构建、Nginx 配置加载、新卷初始化和完整容器启动。数据库集成测试与 race 检查未在本轮执行。

上述为早期基础设施阶段记录。当前前端已实现阅读闭环，设计已 Frozen v1.5；最新范围见 frontend-progress.md，不以早期 Draft 或未实现描述作为当前结论。


冻结前历史隔离验证：171 个顶层测试通过（含子测试 237 个通过事件）。当时 Docker 下载受阻、契约仍为 Draft；该状态已被后续冻结与验证记录取代，见 [verification-2026-11-12.md](verification-2026-11-12.md)。


冻结前历史补交（Xray 代理复验）：生产镜像、隔离 Compose、Nginx、卷写入/持久化、在途请求排空、旧视图升级和重复应用通过；本机 Linux race 当时 171 个顶层测试通过。后续接口说明补齐消除了历史 Spectral warning；具体阶段证据见 [验证记录](verification-2026-11-12.md)。这些历史数字不代表当前新增修复再次跑过全量 race，远程 CI 首次日志仍待取得。


已有实例升级备注：当前 Makefile 数据库门禁仅允许本机测试及隔离库，不能直接作为生产迁移入口。生产升级需另行准备、审查并授权迁移步骤；本轮未迁移任何既有持久库。

2026-11-13 审计整改结果及未闭环项见 [audit-remediation-2026-11-13.md](audit-remediation-2026-11-13.md)。
