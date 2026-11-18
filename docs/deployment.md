# YLReader 自托管部署与生产运维指南

本文档涵盖本地开发编排（`docker-compose.yml`）、生产安全加固编排（`docker-compose.prod.yml`）、HTTPS 反向代理配置、受控数据库迁移、旧版书源导入以及灾备恢复运维闭环。

---

## 1. 密钥与环境变量准备

部署前请先复制根目录 `.env.example` 为 `.env` 并生成独立随机密钥：

```bash
cp .env.example .env
```

必填核心变量说明：

| 变量 | 说明与约束 |
|---|---|
| `JWT_SECRET` | `ENVIRONMENT=production` 下至少 32 字节随机密钥，用于签发访问令牌 |
| `YREADER_HEADER_KEY_primary` | **32 字节随机数据的 Base64 编码**，用于 AES-256-GCM 信封加密 `book_sources.header`；重启、升级与恢复时必须保持一致 |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | 生产编排 `docker-compose.prod.yml` 强制注入，禁止使用默认口令 |
| `REDIS_PASSWORD` | 生产编排 Redis 访问密码，强制通过 `.env` 注入 |
| `SEED_USERNAME` / `SEED_PASSWORD` | 仅在 `users` 表为空时用于创建初始管理员；完成初始化后可在生产 `.env` 中清空以降低凭据暴露面 |
| `TZ` | `app` 与 `sandboxworker-*` 必须显式保持一致（默认 `UTC`，如 `Asia/Shanghai`） |
| `LOCAL_IMPORT_TIMEOUT` | 本地 TXT/EPUB 解析落库超时预算，默认 `90s`（允许 `1s` 至 `10m`） |
| `YREADER_SANDBOX_INPUT_LIMIT_MB` | JS 沙箱单次输入上限（`1..32` MiB，默认 `32`） |
| `YREADER_SANDBOX_INFLIGHT_MB` | 父进程在途编码内存预算（默认 `64` MiB，必须 `>= 2 * InputLimit`） |
| `YREADER_SANDBOX_MAX_CONCURRENT` | JS 沙箱最大并发槽位（`1..32`，默认 `4`；生产双 worker 默认 `2`） |

> **安全提醒**：`scripts/compose-check.env` 仅包含公开测试占位值，**只能用于 CI/本机执行 `docker compose ... config --quiet` 语法核验**，严禁用于实际实例部署。

---

## 2. 本地开发编排 vs. 生产加固编排

### 2.1 本地开发编排（`docker-compose.yml`）

适用于本机开发调试（`app` 默认进程内 `goja` 模式）：

```bash
docker compose --env-file scripts/compose-check.env config --quiet
docker compose up --build -d
```

- 前端入口绑定宿主回环 `http://127.0.0.1:5173`，后端调试端口绑定 `127.0.0.1:8080`。
- PostgreSQL 仅在空数据卷首次初始化时执行 `docs/schema.sql`，并登记版本 `1 / baseline_v1_5`。

### 2.2 生产加固编排（`docker-compose.prod.yml`）

适用于公网自托管与隔离部署：

```bash
docker compose -f docker-compose.prod.yml config --quiet
docker compose -f docker-compose.prod.yml up --build -d
```

生产架构关键安全设计：

1. **双隔离 JS 沙箱 Worker（`sandboxworker-1`、`sandboxworker-2`）**：
   - 基于 `backend/Dockerfile.sandboxworker` 构建静态无 CGO 二进制（`scratch` 基础镜像），通过 `sandboxworker-init` 初始化 `UID 10001`、权限 `0700` 的专用 IPC 目录。
   - 强制启用 `network_mode: "none"`、`read_only: true`、`user: "10001:10001"`、`cap_drop: ["ALL"]`、`security_opt: ["no-new-privileges:true"]`、`mem_limit: 256m`、`memswap_limit: 256m`（禁 Swap）、`pids_limit: 64`、`cpus: 1.0` 与 `restart: on-failure:3`。
   - `app` 通过 `/run/worker-1/worker.sock,/run/worker-2/worker.sock` 接入，一旦 worker 不可用直接失败拒绝启动或返回 `503`，**绝不静默回退进程内执行**。
2. **网络最小权限隔离**：
   - `postgres` 与 `redis` 仅连接 `internal_data`（`internal: true`）内部网络，无公网出网通道且**不向宿主机发布任何端口**。
   - `app` 挂载只读根文件系统与 `/tmp` `noexec,nosuid` tmpfs，对所有书源出网请求执行 `DialContext` 级 SSRF 私网拦截与重定向敏感头剥离。
3. **只暴露前端回环端口**：
   - `frontend` 仅绑定 `127.0.0.1:${YLREADER_HTTP_PORT:-8080}:80`（只读根文件系统 + 最小 tmpfs），不直接向公网暴露后端或数据库端口。

---

## 3. HTTPS 反向代理与 HSTS 建议

公网暴露时，必须在 `frontend` 回环端口前端部署 TLS 反向代理（Nginx / Caddy / Traefik）：

1. **强制 HTTPS 与 HSTS**：开启 TLS 1.2/1.3，并下发 `Strict-Transport-Security: max-age=31536000; includeSubDomains`。
2. **大文件导入与超长预算兼容**：`LOCAL_IMPORT_TIMEOUT` 最大支持 `10m`，且本地书库上传上限为 `100 MiB`。反向代理应配置与内部 `frontend/nginx.conf` 匹配的请求体与超时上限：
   ```nginx
   client_max_body_size 100m;
   proxy_read_timeout 660s;
   proxy_set_header Host $host;
   proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
   proxy_set_header X-Forwarded-Proto https;
   add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
   ```
3. **监控端口隔离**：`/metrics` 仅允许可信内网或监控采集器访问；`/readyz` 探针会实时校验数据库连通性、`schema_migrations` 基线与视图形状、`Redis` 及隔离 `sandboxworker` 健康状态。

---

## 4. 受控数据库升级（`cmd/migrator`）

- 根目录 `Makefile` 中的 `make migrate` 与 `scripts/check-db-target.py` 仍保持测试库安全门禁，仅允许本机回环测试库（`yreader_test` / `yreader_a1_iso_*`），防止误操作持久库。
- 对生产数据库执行结构检查或升级时，请先**完成完整备份**，然后按以下顺序显式执行 `cmd/migrator`：

1. **查看当前迁移账本状态（只读）**：
   ```bash
   DATABASE_URL="postgres://..." go run ./cmd/migrator schema status
   ```
2. **执行结构与基线就绪检查（只读）**：
   ```bash
   DATABASE_URL="postgres://..." go run ./cmd/migrator schema check
   ```
3. **显式确认执行生产升级（单事务 + 咨询锁）**：
   必须在 `DATABASE_URL` 中显式指定 `host`、`port`、`dbname`（且未设置冲突的 `PGHOST`/`PGPORT`/`PGDATABASE`/`PGSERVICE` 等环境变量），并通过 `--production-confirm-db` 二次确认完全一致的库名：
   ```bash
   DATABASE_URL="postgres://yreader:<PASS>@<HOST>:<PORT>/yreader?sslmode=require" \
     go run ./cmd/migrator schema up --production-confirm-db=yreader
   ```
   CLI 会在单事务与 `pg_advisory_xact_lock` 内校验 `schema_migrations` 账本与校验和、幂等应用迁移并在提交后立即执行 `CheckSchema` 验证。升级完成后再检查应用 `/readyz` 返回 `200 OK`。

---

## 5. 旧版 Reader / Legado 书源导入 CLI

可通过 `cmd/migrator import-legado-sources` 将导出的 Legado / 旧版 Reader 书源 JSON 文件（单对象或对象数组，上限 32 MiB）导入指定账户；导入管线要求配置 `YREADER_HEADER_KEY_<KID>` 并自动为所有非空 `header` 执行 AES-256-GCM 信封加密：

```bash
# 先使用 --dry-run 演练（单事务执行完整校验与冲突统计后自动回滚，不写库）
DATABASE_URL="postgres://yreader:<PASS>@<HOST>:<PORT>/yreader?sslmode=disable" \
YREADER_HEADER_KEY_KID=primary \
YREADER_HEADER_KEY_primary="<BASE64_32B_KEY>" \
  go run ./cmd/migrator import-legado-sources \
    --user=admin \
    --file=/path/to/sources.json \
    --overwrite=skip \
    --dry-run

# 确认报告无误后移除 --dry-run 正式导入（可选 --overwrite=update 覆盖同 URL 书源）
```

---

## 6. 灾备恢复运维闭环（`cmd/backup`）

YLReader 内置独立的 `cmd/backup` 灾备校验与受控恢复工具链（详见 `docs/backup-restore-design.md` 及 `docs/backup-controlled-restore-cli-design.md`）：

1. **演练与核对（只读）**：
   - 使用 `backup inspect --input-root <ABS_BACKUP_DIR> --request-key <KEY>` 检查备份归档完整性与清单摘要。
   - 使用 `backup plan --input-root <ABS_BACKUP_DIR> --request-key <KEY> --config-key <KEY>` 生成绑定目标实例与 15 分钟有效期的恢复计划（输出 `plan_digest`）。
2. **受控恢复与状态跟踪**：
   - 执行 `backup restore --input-root ... --plan-digest <SHA256> --confirm-target <UUID> --owner <UUID> --header-keys <ABS_PATH> --pg-restore <ABS_PATH> --timeout 10m`。
   - 恢复过程中通过 `backup status --control-root <ABS_DIR> --operation <UUID>` 与 `backup behavior-status` 审计 CAS 阶段流转与租户数据一致性。
3. **密钥与持久化卷**：
   - 备份归档中的 `header` 仍受原实例 `YREADER_HEADER_KEY_<KID>` 保护，请务必与 Age 恢复密钥、PostgreSQL 物理/逻辑备份及 `storagedata` 书库卷同步离线保管。
   - 已有文件卷或绑定目录须保持 `UID 10001` 可写权限。

---

## 7. 优雅退出与运维注意事项

- `app` 收到 `SIGINT` / `SIGTERM` 后会先在 30 秒内排空在途 HTTP 请求，随后等待后台 Worker（目录刷新、健康检查、整本缓存等）在 30 秒内收尾并释放连接池（Compose `stop_grace_period: 65s`）。
- 当本地 `storagedata`、PostgreSQL 数据卷跨版本持久化迁移时，切勿直接删除数据卷；应先做备份并在隔离测试实例验证后再切流。

---

## 8. 第三方书源与免责声明

YLReader 是纯技术自托管阅读器引擎，**不内置、不分发、不提供任何默认书源、小说文本或侵权内容**。所有第三方 Legado 书源规则均由部署者或使用者自行导入、校验与维护；因使用第三方书源规则抓取或访问外部站点所产生的合规、版权及网络安全责任，均由使用者自行承担。
