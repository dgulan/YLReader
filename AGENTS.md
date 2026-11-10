# AGENTS.md — 新一代自托管阅读器 (Clean-Room Reader) 架构与开发宪章

> **致 AI Coding Agent (DeepSeek Harness / Sub-agents / REA)：**
> 在执行任何代码生成、重构、文件创建或命令调用前，必须严格通读并遵守本宪章。任何违反“净室隔离原则”或“契约先行原则”的操作均视为严重错误。

---

## 1. 项目定位与核心原则 (Core Principles)

本项目是基于 Reader 生态的功能研究，**从零重新设计架构、独立开发的全新自托管阅读器产品**，兼容阅读 3.0 (Legado) 书源生态。

### 参考仓库清单（只读，仅 Stage 1 研究使用）

6 个参考仓库已克隆至项目根下的 `references\` 目录，与线上仓库的对应关系如下：

| 本地目录 | 线上仓库 |
|---|---|
| `D:\YReader\references\warpdotsys_reader-dev` | https://github.com/warpdotsys/reader-dev |
| `D:\YReader\references\changshengyu_openreader` | https://github.com/changshengyu/openreader |
| `D:\YReader\references\givenge_reader-rust` | https://github.com/givenge/reader-rust |
| `D:\YReader\references\1432433gg_reader-next` | https://github.com/1432433gg/reader-next |
| `D:\YReader\references\kukume_legado-web` | https://github.com/kukume/legado-web |
| `D:\YReader\references\changshengyu_reader-dev` | https://github.com/changshengyu/reader-dev |

**路径红线**：所有参考仓库均位于 `D:\YReader\references\` 下，使用绝对路径访问。严禁将 `references\` 下的任何代码复制、粘贴、软链或引入到本项目的 `backend\`、`frontend\`、`crates\` 等源码目录。

### 铁律 1：净室设计原则 (Clean-Room Isolation)

- **只复刻成熟功能与用户体验，绝不盲目复制旧代码**：外部 6 个参考仓库（位于 `D:\YReader\references\`，只读）仅在**阶段 1（研究）**供 REA 插件提取业务逻辑、状态机与规则边界。
- **彻底切断运行时与代码依赖**：严禁将旧项目中的 Kotlin/Java/Rust/旧版 Vue 代码直接复制粘贴到本仓库；严禁沿用旧版基于本地零散 JSON 文件落盘的存储架构。

### 铁律 2：契约先行与克制选型 (Contract-First & Pragmatism)

- **不提前锁死非核心细节**：在编写任何前后端业务代码前，必须先确认 `docs/research/`（研究报告）、`docs/schema.sql`（数据库设计）与 `docs/openapi.yaml`（接口契约）已冻结。
- **渐进式性能优化**：后端优先使用 Go 解决高并发 I/O 与业务逻辑，**不必为了追求极致性能在一开始全部采用 Rust**；仅当特定计算密集型模块（如超大 TXT 分章、高频复杂正则净化）经压测证实成为瓶颈时，才以独立模块或 FFI/WASM 方式引入 Rust。

---

## 2. 六阶段推进门禁 (6-Stage Workflow Gates)

Agent 必须根据当前所处阶段严格限制自身读写边界：

1. **Stage 1: 研究 (Research)** — 仅调用 `REA` 分析 `D:\YReader\references\` 下的 6 个参考仓库，产出物**仅限**写入 `docs/research/*.md`。禁止创建业务源码。
2. **Stage 2: 设计 (Design)** — 产出并评审 `docs/architecture.md`、`docs/schema.sql`、`docs/openapi.yaml`。禁止编写前后端实现代码。
3. **Stage 3: 重构/脚手架 (Scaffold)** — 初始化干净的 Monorepo 工程底座、Linter、Dockerfile 与 `docker-compose.yml`。
4. **Stage 4: 开发 (Develop)** — 按垂直模块优先级推进：`M1 书源解析核心 -> M2 阅读器与书架 -> M3 本地书库与外围同步`。每完成一个子模块必须编写并通过单元测试。
5. **Stage 5: 验收 (Verify)** — 运行黄金样本书源测试集、性能压测与 SSRF/沙箱安全测试，对照原版体验。
6. **Stage 6: 发布 (Release)** — 构建多阶段生产级容器镜像，提供旧版 Reader 数据平滑导入 CLI 工具。

---

## 3. 技术栈规范与红线 (Tech Stack Specification)

### 3.1 前端架构 (`/frontend`)

- **核心框架**：Vue 3 (强制使用 `<script setup lang="ts">` Composition API) + TypeScript (开启 `strict: true`) + Vite + Pinia。
- **双层 UI 策略（严禁混用）**：
  1. **管理与外围视图**（书架管理、书源列表、书源调试台、系统设置、用户中心）：使用 **Element Plus**，超过 100 条数据的列表（如书源管理、大目录）必须使用虚拟滚动组件。
  2. **核心阅读器视图**（正文排版 Canvas/DOM、翻页动画、移动端沉浸式交互抽屉）：**严禁使用重型 Element Plus 弹窗/布局组件**，必须采用原生 CSS Grid/Flex + 轻量原子化 CSS + CSS Variables（支持日间/夜间/羊皮纸/护眼主题无缝切换）自研，确保 60/120Hz 丝滑翻页。
- **本地离线存储**：Pinia 仅管理运行时 UI 与会话状态；当前阅读章节及前后预加载章节（3~5 章）必须结合 **IndexedDB (推荐 `Dexie.js`)** 进行本地持久化缓存。

### 3.2 后端架构 (`/backend`)

- **主开发语言**：Go (1.22+)，采用清晰的分层架构（Handler -> Service -> Repository / Engine）。
- **核心书源引擎选型（候选基准）**：
  - HTML/CSS 选择器：`PuerkitoBio/goquery`
  - XPath 解析：`antchfx/htmlquery`
  - JSONPath 解析：`tidwall/gjson`
  - 内置 JS 规则沙箱：纯 Go 实现的 **`dop251/goja`**（严禁使用 CGO 绑定 V8 以保持静态编译与轻量容器化）。必须为每个书源执行上下文注入安全的 `java.*` / `source.*` 桥接方法，并强制设置 **执行超时 Context** 与 **防 SSRF 内网 IP 过滤器**（禁止书源请求 `127.0.0.1`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16` 等私有网段）。
- **Rust 候选扩展 (`/crates`，按需启用)**：在 Stage 4 前期保持为空，仅在后续性能调优阶段用于独立的高性能文本处理模块。

### 3.3 数据与存储层 (Storage Layer)

- **PostgreSQL (主数据库)**：
  - 存储用户、书架、阅读进度（精确到 `chapter_index` + `char_offset` + `updated_at` 以解决多端冲突）、替换净化规则。
  - **Legado 书源存储规范**：核心检索字段（如 `book_source_url`、`book_source_name`、`book_source_group`、`enabled`、`weight`）提取为标准关系列；复杂且多变的解析规则（`rule_search`、`rule_toc`、`rule_content` 等）统一存入 **`JSONB`** 字段并建立 GIN 索引。
- **Redis (缓存与异步队列)**：
  - 缓存高频搜索结果、书籍目录树快照（带 TTL）及域名级抓取限流令牌（Rate Limiter）。
  - 驱动后台异步任务（定时追更巡检、整本小说缓存下载）。
- **对象与文件存储 (`StorageProvider` 接口抽象)**：
  - 代码中禁止硬编码本地文件路径操作。必须定义统一的 `StorageProvider` 接口，同时实现 **LocalFS 驱动**（适配单机 NAS / Docker Volume 直接挂载）和 **S3 驱动**（适配 MinIO / 云端对象存储），用于存放本地上传的 EPUB/TXT、书籍封面、自定义字体与备份包。

---

## 4. 工程目录结构约束 (Monorepo Layout)

Agent 在创建或修改文件时，必须严格遵循以下目录拓扑，不得随意在根目录散落源码文件：

```text
D:\YReader\
├── AGENTS.md                   # 本架构宪章（Agent 必读）
├── docker-compose.yml          # 容器编排配置 (App + PostgreSQL + Redis)
├── Makefile                    # 统一构建、测试、Lint 与迁移脚本
├── references\                 # 【只读】6 个参考仓库，仅 Stage 1 研究使用
│   ├── warpdotsys_reader-dev\
│   ├── changshengyu_openreader\
│   ├── givenge_reader-rust\
│   ├── 1432433gg_reader-next\
│   ├── kukume_legado-web\
│   └── changshengyu_reader-dev\
├── docs\                       # 阶段 1 & 阶段 2 核心交付物
│   ├── research\               # REA 逆向分析 6 大仓库产出的研究报告
│   ├── architecture.md         # 系统架构与书源沙箱设计文档
│   ├── schema.sql              # PostgreSQL 权威 DDL 定义
│   └── openapi.yaml            # 前后端 API 契约规范 (OpenAPI 3.0)
├── backend\                    # Go 后端主工程
│   ├── cmd\
│   │   ├── server\             # Web API 与后台任务主入口
│   │   └── migrator\           # 旧版 Reader 数据导入与 DB 迁移 CLI
│   ├── internal\
│   │   ├── api\                # HTTP Handlers, 中间件, 路由注册
│   │   ├── service\            # 核心业务逻辑 (书架、用户、同步、TTS)
│   │   ├── repository\         # PostgreSQL & Redis 数据访问层
│   │   ├── pkg\storage\        # StorageProvider 抽象 (LocalFS & S3 实现)
│   │   └── engine\             # 【核心】Legado 书源解析与抓取引擎
│   │       ├── parser\         # CSS / XPath / Regex / JSONPath 解析器
│   │       ├── sandbox\        # goja JS 沙箱与 java.* 桥接 API
│   │       └── fetcher\        # HTTP 客户端池、自动编码转换(GBK/UTF8)、反爬限流
│   ├── go.mod
│   └── Dockerfile
├── frontend\                   # Vue 3 前端工程
│   ├── src\
│   │   ├── api\                # 基于 openapi.yaml 的强类型接口请求层
│   │   ├── stores\             # Pinia 状态仓库 (user, bookshelf, readerConfig)
│   │   ├── db\                 # IndexedDB 本地章节离线缓存层
│   │   ├── views\
│   │   │   ├── admin\          # Element Plus 驱动：书源管理、调试台、设置
│   │   │   ├── bookshelf\      # 响应式书架视图
│   │   │   └── reader\         # 原生高性能视图：阅读器排版、手势与主题引擎
│   │   └── components\
│   ├── package.json
│   └── Dockerfile
└── crates\                     # [预留] Rust 特定高性能模块候选目录

## 5. **Windows PowerShell UTF-8 编码铁律**：
本环境为 Windows，PowerShell 5.1 默认使用 ANSI/GBK 编码。你在调用任何 PowerShell 命令读取或写入包含中文的文件、或解析命令标准输出时，必须在命令开头显式加上 `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $PSDefaultParameterValues['*:Encoding'] = 'utf8'; `，或显式指定 `-Encoding UTF8`（如 `Get-Content -Encoding UTF8`），严禁裸调导致 UTF-8 误判为乱码。