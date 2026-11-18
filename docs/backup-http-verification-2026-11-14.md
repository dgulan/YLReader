# 恢复后 HTTP 验收记录（2026-11-14）

依据 [HTTP 隔离验收协议](backup-http-acceptance-design-2026-11-14.md)，本轮已把完整恢复链连接到现有 HTTP Router、认证与阅读服务。仍是独立合成实验，没有公开恢复 CLI/API、常驻服务、部署、提交、推送或合并。

## 已验证的行为

- 官方 pg_dump 归档包含两位用户、现有 Argon2 参数生成的密码哈希、真实登录产生的两份旧会话、两代加密 header、两本本地书、进度、书签和运行中任务。完整 age 验证、数据事务、header 门禁及 publication verified 均先于 HTTP。
- 独立新 JWT 随机密钥用于目标 AuthService。两位用户的旧 access token 经 HTTP 返回 401，旧 refresh token 返回 401；另用旧 JWT 密钥在已恢复数据库执行 Authenticate，同样因 token_version 变化拒绝旧 access token。旧 refresh 检查在新登录之前，避免其复用防护影响新会话。
- 健康和 readiness 返回 200，并明确包含 PostgreSQL、Redis、schema 三项 ok。两位用户均可重新登录、刷新新令牌和读取个人信息；错误密码返回 401。
- 每位用户的书架仅包含其本人书籍；目录包含第 0 章，本地正文通过实际已发布对象根读取，含中文和 emoji，cached=false。进度仍为 char_offset=17、version=7，书签备注 `unicode: 中文 😀` 保留。双方访问对方正文均返回 404。
- 两个独立 Redis 容器在同一无外网网络命名空间的不同回环端口运行。源写入的唯一实验 key 在目标 miss；目标写入同名 key 后双方保留各自不同值。目标客户端也用于真实 ContentService 和 readiness，无内存降级，无 FLUSH。由于本地书正文绕过 Redis，不将正文成功冒充缓存隔离证明。
- HTTP 期间未启动 worker 或 scheduler；结束时没有 queued/running 任务，拒绝外部请求的抓取器调用计数为 0。日志丢弃，断言不输出 token、header、DSN 或响应正文。
- 实验证据仅在全部断言成功后生成，绑定 operation/plan/package/manifest/target/publication。脚本严格校验字段白名单、UUID、摘要格式和十项检查集合。scope 为 isolated-http-fixture，operation_phase 仍为 validating，ready_for_cutover 和 deployment_authorized 均为 false。

HTTP 登录和刷新会创建新会话并更新 last_login_at；恢复前后原始表计数的门禁在启动临时 HTTP listener 前执行。实验不是对运行中数据库强行维持归档 sessions 行数。

## 本轮结果与复现

- 最终运行目录：`.toolchain/backup-restore-db-d58488016916/`。tests.txt、result.json、http-evidence.json 保存结果；result.json 同时记录实际镜像、测试二进制及原始合成 fixture SHA-256。
- 真实隔离集成 4 个顶层、27 个子测试，共 31 个通过事件；0 失败、0 跳过。原有事务、对象、header、发布故障窗口全部保留，新加 headerhttp 贯穿成功链。
- PostgreSQL 16.15，数据 tmpfs、无宿主端口、无外网；测试 UID 10001，源码、文档、fixture 只读挂载。两个 Redis 使用独立 tmpfs 数据目录。四个本轮自有容器均按标签核对后清理，结果全部 true。
- Windows backup/migration/storage/crypto 回归：46 个顶层、168 个含子测试通过事件，0 失败/跳过；日志 `.toolchain/backup-http-windows.jsonl`。Windows 不执行 Linux 专用 HTTP 集成或安全存储驱动。
- Linux integration 标签下 backup 包 vet 通过；65 项契约检查通过，16 份冻结摘要匹配；Python 脚本编译及 Go 格式检查通过。未修改公开 OpenAPI、DDL、已发布迁移或冻结备份格式。本轮没有新增生产实现，也未将此前 Linux race 结果称为新增 HTTP 用例的 race 验证。

构建 Linux 集成测试二进制（工作目录 backend，使用仓库记录的 Go 1.26.11 及模块缓存）：

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-restore.test ./internal/backup
```

在项目根运行 `python scripts/verify-backup-restore-db.py`。脚本只使用本地已有镜像并拒绝拉取；默认 fixture 是已有的合成原型归档，可通过 --fixture/--binary 显式指定。需要 Docker 权限，不能对业务集群或生产配置运行替代命令。

首次运行在容器隔离检查处发现 Docker 将 network 引用规范化为完整容器 ID；脚本现仅接受本轮 PG 名称或其精确 ID，没有放宽到任意 container 网络。该失败运行未进入恢复用例，四项清理均为 true。修正后完整链和最终证据校验通过。

## 下一步

先设计受控恢复 CLI 与正式交接报告契约：显式密钥和实例配置、预览/执行边界、失败后只读诊断、报告持久登记，以及 ready_for_cutover 所需的目标配置和缓存身份核验。当前测试证据不是这一正式报告；不能凭它接管中断操作、清理保留对象或启动真实实例。

真实移动设备、合法远程书源、断网恢复、多端长期使用、大数据性能、S3 和不同 NAS 文件系统仍未由本轮覆盖。completed、部署及服务切换继续关闭。
