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

### 生产环境部署（`docker-compose.prod.yml`）

```bash
# 下载并解压到固定目录 YLReader（后续升级覆盖同一目录即可，避免数据目录分散）
mkdir -p YLReader
curl -fsSL https://github.com/dgulan/YLReader/archive/refs/tags/v1.1.0.tar.gz | tar -xz --strip-components=1 -C YLReader
cd YLReader

# 1. 首次运行：自动生成 .env 并填充随机 JWT_SECRET（32B hex）和 YREADER_HEADER_KEY_primary（32B base64）
chmod +x scripts/deploy.sh
./scripts/deploy.sh

# 2. 按提示编辑 .env：填写 POSTGRES_PASSWORD、REDIS_PASSWORD、SEED_USERNAME、SEED_PASSWORD
#    （国内构建可取消 .env 中 GOPROXY / NPM_REGISTRY 注释以加速依赖拉取）

# 3. 再次运行：创建数据目录、启动 postgres/redis、执行 /migrator 迁移并后台拉起全部容器
./scripts/deploy.sh
```

**常用 `.env` 可选配置：**

```dotenv
YLREADER_BIND_IP=127.0.0.1   # 局域网直连请改为 0.0.0.0
YLREADER_HTTP_PORT=8080      # 按需修改访问端口
```

**访问方式说明：**

- **默认安全绑定（`YLREADER_BIND_IP=127.0.0.1`）**：服务默认仅监听宿主机回环 `127.0.0.1:8080`，建议通过 TLS 反向代理对外提供 HTTPS，或在本地通过 SSH 隧道安全转发访问：
  ```bash
  ssh -N -L 8080:127.0.0.1:8080 <user>@<server-ip>
  # 随后在本地浏览器访问 http://127.0.0.1:8080
  ```
- **局域网直连**：将 `.env` 中 `YLREADER_BIND_IP` 设为 `0.0.0.0`（并按需放行防火墙端口），再执行 `./scripts/deploy.sh` 重建生效。

完整架构与受控升级、灾备恢复说明见 [docs/deployment.md](docs/deployment.md)。

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
