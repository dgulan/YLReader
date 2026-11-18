# YLReader

基于干净架构从零设计的自托管小说阅读器（Go / PostgreSQL / Redis + Vue 3 / PWA），兼容阅读 3.0（Legado）书源规则与本地 TXT / EPUB 书库管理。

- 开源协议：[MIT License](LICENSE)
- 安全策略与架构边界：[SECURITY.md](SECURITY.md)
- 自托管部署与生产运维：[docs/deployment.md](docs/deployment.md)
- 系统架构设计：[docs/architecture.md](docs/architecture.md)
- 接口契约（OpenAPI 3.0）：[docs/openapi.yaml](docs/openapi.yaml)
- 数据库权威结构：[docs/schema.sql](docs/schema.sql)

## 核心能力与安全特性

- **双层前端阅读体验**：管理控制台使用 Element Plus 驱动；核心阅读器采用原生 DOM/CSS Grid/Flex、主题变量、离线 IndexedDB 预加载缓存、PWA 静态应用壳与移动端安全区触控手势。
- **可控书源解析沙箱**：纯 Go `goja` JS 运行时（无 CGO/V8），支持 CSS / XPath / JSONPath / 正则解析，内置超时、输入输出与在途内存预算控制，并在生产编排中通过独立 `network_mode: "none"` `sandboxworker` 容器隔离执行。
- **SSRF 与凭据防护**：`DialContext` 级私有/保留网段拦截、重定向敏感头剥离、Argon2id 口令哈希及 `book_sources.header` AES-256-GCM 信封加密。
- **完整运维工具链**：内置 `cmd/migrator`（受控生产升级与旧版 Legado 书源导入）与 `cmd/backup`（备份校验与受控恢复）。

## 快速开始

### 生产推荐（隔离沙箱 + 内网数据库）

```bash
cp .env.example .env
# 编辑 .env，填写独立生成的 JWT_SECRET、YREADER_HEADER_KEY_primary、POSTGRES_PASSWORD、REDIS_PASSWORD 等密钥
docker compose -f docker-compose.prod.yml config --quiet
docker compose -f docker-compose.prod.yml up --build -d
```

> 请在 `frontend` 暴露的回环端口（默认 `127.0.0.1:8080`）前端配置 TLS 反向代理并启用 HSTS，详见 [docs/deployment.md](docs/deployment.md)。

### 本地开发（`docker-compose.yml`）

```bash
docker compose --env-file scripts/compose-check.env config --quiet
docker compose up --build -d
```

开发测试检查：

- 后端：`cd backend && go test -mod=readonly ./...`
- 前端：`cd frontend && npm run type-check && npm test && npm run build`
- 契约与目标门禁：`python scripts/verify-backup-contract.py`、`python scripts/test-db-guard.py`

## 免责声明

YLReader 仅提供自托管阅读软件与本地格式解析能力，**不包含或预设任何第三方书源或作品内容**。使用者导入、调试或抓取外部书源站点时，请严格遵守所在司法辖区的版权法律法规与目标站点服务条款。
