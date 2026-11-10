# givenge_reader-rust 研究报告

> 仓库：`D:\YReader\references\givenge_reader-rust` | 线上：https://github.com/givenge/reader-rust
> 技术栈：Rust 1.0.8 / edition 2021 / Axum 0.7 + Tokio / sqlx 0.7（**SQLite only**，`migrate`）/ reqwest 0.12（rustls-tls，gzip/brotli/deflate/cookies）/ **rquickjs 0.7（QuickJS，执行 `@js:` 规则）** / scraper 0.20 + `jsonpath_lib` 0.3 + `sxd-xpath` 0.4 / `regex` 1.x（**线性时间引擎**）/ encoding_rs / aes + cbc / feed-rs / zip；前端 Vue 3.5 + Vite 8（`frontend/`），另保留 `web/` 旧 Vue2 客户端
> 代码规模：331 个文件；最大文件 `src/api/handlers/book.rs` 3,185 行、`src/parser/rule_engine.rs` 2,508 行、`src/service/book_service.rs` 1,717 行、`src/service/user_service.rs` 1,306 行、`src/parser/html.rs` 847 行、`src/crawler/url_analyzer.rs` 665 行、`src/parser/js.rs` 528 行；`src/storage/db/migrations/` 仅 3 个 SQL（20/14/43 行）
> 分析范围与方法：读 `Cargo.toml`、`src/parser/{rule_analyzer,rule_engine,html,js}.rs` 关键段、`src/model/book_source.rs`、`src/storage/db/migrations/*.sql`、`src/storage/cache/file_cache.rs`、`src/crawler/{url_analyzer,http_client,fetcher}.rs` 关键点、`src/api/router.rs` 路由清单；用 `grep` 验证 JS 限额/SSRF/重试/模式识别。未覆盖：`service/local_epub_book.rs`(1,213)/`local_txt_book.rs`、AI 相关 handler、`web/` 旧前端、`tests/yckceo_live_sources.rs` 的真实源清单。

## 0. 一句话结论

这是 6 个仓库里**最「现代 Rust Web 服务」、也是「最像 Reader 3.0 Web API 平替」的实现**：Axum + sqlx 迁移 + `/reader3/*` 兼容路由 + rquickjs 真跑 JS，且用 Rust `regex` 天然免疫 ReDoS；但它的规则引擎是一个 2,508 行的单文件、`book.rs` 3,185 行、JS 无超时无内存上限、`java.ajax` 无 SSRF、书架数据塞在 `json_documents` 的 JSON blob 里——**架构上可学的是「API 兼容层 + 迁移 + 线性正则」，不可学的是它的上帝文件与安全空白**。

## 1. 书源解析：规则类型与执行模型

### 1.1 支持的规则类型（逐项判定）

| 规则类型 | 判定 | 说明 | 证据 |
|---|---|---|---|
| CSS（默认） | 支持 | `scraper` crate；`@` 尾部操作拆分 | `rule_engine.rs:37-39`、`html.rs:414` |
| `@css:` 显式 | 支持 | 同样走 `ParseMode::Css` | `rule_engine.rs:37` |
| `@@` 转义 | 支持 | `find_next_delimiter` 明确跳过 `@@`（不当分隔符） | `rule_analyzer.rs:79` |
| `@XPath:` / `//` / `./` | 支持 | `sxd-xpath`；`/` 与 `./` 都判 XPath | `rule_engine.rs:40-43`、`:55-57` |
| `@json:` / JSONPath | 支持 | `jsonpath_lib`；`$.`/`$[` 自动判定，内容像 JSON 也走 JSONPath | `rule_engine.rs:44-46`、`:58-60`、`:66-76` |
| `##` 正则替换 | 支持 | 尾部 `##pattern##replacement[##first]` | `rule_engine.rs`（`parse_transform` 段）；`model/rule.rs` |
| 纯正则（首字符 `:`） | 支持 | `detect_mode` 直接判 `Regex` | `rule_engine.rs:61-63` |
| **`@regex:` 显式前缀** | **支持（超出 Legado 标准）** | 显式正则模式，`@Regex:` 亦可 | `rule_engine.rs:47-49` |
| `:regex` 后缀 | 不支持 | 无该写法 | 未发现（已验证：`grep ":regex"`） |
| `||` / `&&` / `%%` | 支持 | `split_top_level` 平衡扫描；`%%` 交错 | `rule_analyzer.rs:7-86`；`html.rs:454`、`:669` |
| `@put:` / `@get:` / `{{ }}` | 支持 | 有独立 `@put`/`@get` 处理分支与正则 | `rule_engine.rs:1450-1486`、`:1807-1919` |
| `@js:` / `<js>` / `js:` | 支持 | rquickjs 执行；`js:` 无 `@` 也接受 | `rule_engine.rs:50-52`；`js.rs:53-118` |
| `$n` 捕获 | 支持 | 正则模式取分组 | `rule_engine.rs:100-160`（Regex 分支） |
| `!` 排除 / `[-1]` / 区间索引 | 部分支持 | 未见 Legado 的 `!`/`[]` 索引语法；CSS 交给 scraper | 未发现（已验证：`grep "indexDefault\\|findIndexSet"` 无命中） |
| `@href`/`@text`/`@html`/`@textNodes`/`@ownText` | 支持 | `text/@text`、`html/@html`、`textNodes`、`ownText` 均有显式分支 | `html.rs:484-516`、`:494`、`:502-509` |
| 章节分页 `nextContentUrl` | 支持 | `ContentRule.next_content_url` | `model/rule.rs` |
| `${key}` URL 占位符 | 支持 | `searchUrl` 用 `${key}` 而非 `{{key}}`（与 Legado 不同） | `AGENTS.md`「searchUrl/exploreUrl (with `${key}` placeholders)」；`url_analyzer.rs:239` |
| `jsLib`（书源级共享 JS） | 支持 | `with_js_lib(source.js_lib, ...)` 包裹整个解析 | `js.rs:44`、`:135`；`book_source.rs:19` |
| `coverDecodeJs` | 支持 | 封面解码脚本字段 | `book_source.rs:25` |
| `ruleReview`（段评） | 部分支持（模型有、执行链未验证） | `ReviewRule` 模型已接 | `book_source.rs:5`、`:43` |
| base64/AES/DES | 部分支持 | `java.*` 桥里可见 `md5Encode/timeFormat/androidId/get` 等；DES/AES 未在 `js.rs` 前 200 行出现 | `js.rs:185-204` |

### 1.2 执行模型（关键）

1. **模式识别是「前缀优先 + 内容兜底」**：显式前缀（`@css:/@xpath:/@json:/@regex:/@js:`）→ 隐式前缀（`/`、`./`、`$.`、`$[`、`:`）→ 内容像 JSON 就 JSONPath → 否则 CSS（`rule_engine.rs:33-80`）。**注意它把 `:` 前缀判为 Regex**，这与 Legado 的「首字符 `:` 仅 `getElement` 走正则」不同，属于方言差异。
2. **组合规则拆分子句，不是完整 AST**：`split_top_level` 用 `square/paren/brace` 深度 + 引号 + 转义状态机寻找顶层分隔符（`rule_analyzer.rs:31-86`），并专门处理 `@@`（`:79`）。这是比 Kotlin 版 `RuleAnalyzer` 更简洁的同构实现。
3. **每个 stage 有 5 条独立实现路径**：`search_books_{json,xpath,js,regex,html}`（`rule_engine.rs:92-104`），详情/目录/正文各自再复制一遍——**这是 2,508 行的根因**：模式 × 阶段 = 约 20 条近似代码路径。
4. **JS 执行模型**：
   - 每次求值 `Runtime::new()` + `Context::full()`（`js.rs:129-130`），**无跨规则共享运行时**，天然避免「前一条规则污染后一条」；代价是每次都要编译 `jsLib`（`:321`）。
   - 注入的全局：`input/result/src/base_url/baseUrl/key/page/url/source{key,getKey}/cookie{removeCookie}/cache{get,put}/java{ajax,md5Encode,timeFormat,androidId,deviceID,get,...}`（`js.rs:137-204`）。
   - **没有超时、没有中断、没有内存上限**：`grep "Interrupt|set_interrupt_handler|max_steps|memory_limit"` 在 `parser/js.rs` 无命中；`Runtime::new()` 直接执行（`:129`）。
   - `java.ajax` 走 Rust 侧 HTTP，**没有复用书源网络策略**（`crawler/` 里也未见私网判断，见 §1.2.6）。
5. **网络层**：
   - `reqwest::Client` 超时来自配置 `REQUEST_TIMEOUT_SECS`（默认 15，`app/config.rs:31`），UA 硬编码 Chrome 120（`http_client.rs:14`），支持 gzip/brotli/deflate（`Cargo.toml`）；
   - URL 选项 `retry` 默认 2（`url_analyzer.rs:40`、`:70-71`），`fetcher.rs:76` 按 `req.retry` 循环；无退避（直接重试）；
   - **SSRF 防护未发现**（已验证：`grep "is_private|loopback|127.0.0.1"` 于 `src/crawler` 只命中注释/超时/UA，无地址过滤）；`reqwest` 默认跟随 10 次重定向（未显式覆盖）。
6. **正则引擎是 Rust `regex` crate**：线性时间、无回溯 → **天然免疫 ReDoS**，这是相对 Kotlin/Java 三个实现的实质优势（但也不支持 backreference/lookaround，会拒绝部分 Legado 正则）。

### 1.3 规则边界与失败语义

| 场景 | 行为 | 证据 |
|---|---|---|
| 规则模式无法识别 | 默认当 CSS，交给 scraper；选择器语法错时返回空 | `rule_engine.rs:78-79` |
| JSON 内容但规则不是 `$.`/`$[` | 先尝试 `serde_json` 解析成功即按 JSONPath | `rule_engine.rs:67-75` |
| 搜索列表为空 | 若书源无 `bookUrlPattern`，尝试「详情页兜底」把当前页当一本书 | `rule_engine.rs:106-114` |
| JS 抛错 | `anyhow::Result` 向上传播，handler 转 5xx/错误 JSON | `js.rs:53-118` |
| JS 死循环 | **无法中断**（无 interrupt handler），只能等外层 HTTP 超时（若有） | `js.rs:129`；`app/config.rs:31` |
| 请求失败 | 按 `retry` 重试（默认 2 次），无退避 | `fetcher.rs:76` |
| 缓存未命中 | `FileCache.get` 返回 `Ok(None)`，由上层回源 | `file_cache.rs:18-30` |
| 规则调试台 | 未见独立 debug API；`/reader3/bookSourceProxy` 提供「代理抓取」 | `router.rs:44` |

## 2. 数据模型

### 2.1 表结构（sqlx migrations）

| 表 | 列 | 说明 |
|---|---|---|
| `book_sources` | `book_source_url` **PK**、`book_source_name`、`json`、`updated_at` | 书源整段 JSON 存 `json` 列，URL 做主键 |
| `book_cache` | `book_url` **PK**、`json`、`updated_at` | 书籍详情缓存整段 JSON |
| `chapter_cache` | `(book_url, chapter_index)` **PK**、`file_path`、`updated_at` | **元数据在 DB、正文在文件**，DB 存路径 |
| `users` | `username` PK、`password`、`salt`、`token`、`last_login_at`、`created_at`、`enable_webdav`、`enable_local_store`、`enable_ai_model`、`is_admin` | 密码 + 盐（未用 bcrypt/argon2） |
| `user_sessions` | `(username, token)` PK、`expire_at`，`FK users ON DELETE CASCADE` + `expire_at` 索引 | 有外键级联与过期索引 |
| `json_documents` | `(namespace, name)` PK、`json`、`updated_at` | **书架/分组/书签等「文档型」数据的通用容器** |
| `ai_book_memories` | `(user_ns, book_key)` PK、`book_url`、`json`、`updated_at` + `user_ns` 索引 | AI 阅读记忆 |

### 2.2 持久化形态与兼容策略

- 书源：**`book_source_url` 主键 + 整段 JSON**（`0001_init.sql:1-6`）。`BookSource` 的 `deserialize_rule_option` 允许 `ruleSearch` 既可以是对象、也可以是**字符串化的 JSON**（`book_source.rs:60-80`），因此能同时吃 Legado 的两种导出形态。
- 书架：`json_documents(namespace='bookshelf', name='...')`——**把整个书架塞进一个 JSON 文档**，每次改动全量重写；没有书的独立表、没有进度列、无法用 SQL 查「最近在读」。
- 章节缓存：DB 只存 `file_path`（`0001_init.sql:14-19`），正文由 `FileCache` 写文件（`file_cache.rs:40-45`），路径含 `user_ns/book_key/chapter_key`。
- 迁移：sqlx `migrate`，只有 `0001/0002/0003` 三个版本，**没有 down 脚本**；`0002_add_user_ns.sql` 只做「加 `user_ns` 列」。
- 约束：`book_sources` 用 URL 主键（天然唯一）；`user_sessions` 有外键级联；`json_documents` 无外键、无 schema 校验。

### 2.3 与 Legado 书源 JSON 的字段映射

`model/book_source.rs:9-50` 一次列全：

| Legado 键 | Rust 字段 | 证据 |
|---|---|---|
| `bookSourceName` | `book_source_name` | `:10` |
| `bookSourceGroup` | `book_source_group` | `:11` |
| `bookSourceUrl` | `book_source_url` | `:12` |
| `bookSourceType` | `book_source_type` | `:13` |
| `bookUrlPattern` | `book_url_pattern` | `:14` |
| `customOrder` | `custom_order` | `:15` |
| `enabled` / `enabledExplore` | `enabled` / `enabled_explore` | `:16-17` |
| `enabledCookieJar` | `enabled_cookie_jar` | `:18` |
| `jsLib` | `js_lib` | `:19` |
| `concurrentRate` | `concurrent_rate` | `:20` |
| `header` | `header` | `:21` |
| `loginUrl` / `loginUi` / `loginCheckJs` | `login_url` / `login_ui` / `login_check_js` | `:22-24` |
| `coverDecodeJs` | `cover_decode_js` | `:25` |
| `lastUpdateTime` / `weight` / `respondTime` | 同名字段 | `:26-28`、`:46-47` |
| `exploreUrl` / `exploreScreen` | `explore_url` / `explore_screen` | `:29-30` |
| `ruleExplore` | `rule_explore` | `:31-32` |
| `ruleSearch` | `rule_search` | `:34-35` |
| `ruleBookInfo` | `rule_book_info` | `:36-37` |
| `ruleToc` | `rule_toc` | `:38-39` |
| `ruleContent` | `rule_content` | `:40-41` |
| `ruleReview` | `rule_review` | `:42-43` |
| `bookSourceComment` / `variableComment` | 同名字段 | `:44-45` |
| `loadWithBaseUrl` / `singleUrl` | 同名字段 | `:48-49` |

> 结论：**字段覆盖比 OpenReader 更全（含 `jsLib`/`loginUi`/`coverDecodeJs`/`ruleReview`/`exploreScreen`）**，但 `ruleReview` 是否真正执行未在解析器里看到（只到模型层）。

## 3. 状态机

### 3.1 搜索 → 详情 → 目录 → 正文

```mermaid
stateDiagram-v2
    [*] --> DetectMode
    DetectMode --> CSS: 默认
    DetectMode --> XPath: / 或 ./
    DetectMode --> JSONPath: $. / $[ / 内容是 JSON
    DetectMode --> Regex: : 或 @regex:
    DetectMode --> JS: js: / @js: / <js>
    CSS --> ParseList
    XPath --> ParseList
    JSONPath --> ParseList
    Regex --> ParseList
    JS --> RuntimeNew: Runtime::new() 每次新建
    RuntimeNew --> ParseList: 无超时/无中断
    ParseList --> Empty: 列表为空且无 bookUrlPattern
    Empty --> DetailFallback: 把当前页当一本书
    ParseList --> Detail: 选中 SearchBook
    Detail --> TOC: 解析 ruleBookInfo
    TOC --> Content: 解析 ruleToc（可 nextTocUrl 翻页）
    Content --> CacheMiss: FileCache.get -> None
    CacheMiss --> Fetch: reqwest（timeout=config，retry=2）
    Fetch --> ParseContent: 规则求值
    ParseContent --> CachePut: 写文件 + chapter_cache 行
    CachePut --> [*]
```

证据：`rule_engine.rs:33-114`（模式识别/兜底）、`js.rs:129`（Runtime 新建）、`fetcher.rs:76`（重试）、`file_cache.rs:18-45`（缓存读/写）。

### 3.2 缓存与一致性

- 章节缓存：`chapter_cache` 行 + 文件双写，无事务（先写文件再写 DB 或反之，代码顺序决定孤儿）；`updated_at` 存在但**没有 TTL/失效逻辑**（`file_cache.rs` 无过期判断）。
- 书籍缓存：`book_cache` 整段 JSON，`updated_at` 仅记录，不参与失效。
- 书源：`book_sources.updated_at`，无版本号；导入即覆盖。
- 并发：sqlx SQLite 连接池（默认），未见显式 WAL/busy_timeout 配置（`db/mod.rs` 21 行）——**高并发写会 `database is locked`**。

## 4. 已知架构缺陷

| 严重度 | 缺陷 | 影响 | 证据 | 根因 |
|---|---|---|---|---|
| P0 | JS 无超时/无中断/无内存上限 | 一条 `while(true){}` 即可永久占用一个 tokio worker；QuickJS 堆可无限增长 | `js.rs:129-130`（`Runtime::new()` 直接 eval）；`grep Interrupt/memory_limit` 无命中 | 只做了「隔离运行时」，没做「资源配额」 |
| P0 | 无 SSRF 防护 | 书源/`java.ajax` 可访问 `127.0.0.1`、内网、云元数据 `169.254.169.254` | `src/crawler/*` 无地址过滤（已验证 grep）；`js.rs:188` `java_ajax` 直接发请求 | 直接使用 reqwest 默认 client |
| P1 | `book.rs` 3,185 行 + `rule_engine.rs` 2,508 行 | 改一个规则语义要动 5 条路径；回归只能靠人肉 | 文件行数；`rule_engine.rs:92-104` 五分支重复 | 「模式 × 阶段」复制粘贴式实现 |
| P1 | 书架存 `json_documents` 单文档 | 无法分页/索引/并发更新；两设备同时改书架 = 后写覆盖 | `0003_users_and_account_documents.sql:25-31` | 为快速兼容 Reader Web API，选了文档存储 |
| P1 | 密码只用「salt + 哈希」自实现 | 未用 bcrypt/argon2，迭代次数未知；易被离线爆破 | `0003...sql:1-12`（`password`/`salt` 列） | 早期实现，未升级 KDF |
| P1 | 章节缓存文件 + DB 双写无事务 | 崩溃/删除时留孤儿文件或指向不存在文件的 `file_path` | `file_cache.rs:40-45`；`0001_init.sql:14-19` | 没有统一的 StorageProvider 抽象 |
| P2 | 迁移仅 3 个、无 down | 升级可回滚性差；schema 演进受限 | `src/storage/db/migrations/` | 早期项目 |
| P2 | `regex` crate 不支持 backreference/lookaround | 部分 Legado 书源正则直接编译失败 | `Cargo.toml` `regex = "1"` | 用线性引擎换 ReDoS 免疫 |
| P2 | 测试覆盖薄 | `tests/js_compat.rs` 仅 14 行，JS 行为几乎没有回归保护 | `tests/` 清单 | 以手工/真实源测试为主 |
| P2 | `jsLib` 每次 eval 重新编译 | 大 `jsLib` 书源每次解析都编译一遍，CPU 浪费 | `js.rs:321` | 无编译缓存（rquickjs 可缓存 bytecode） |
| P2 | 未显式限制重定向 | reqwest 默认 10 次，可能被用作跳转链探测 | `crawler/http_client.rs`（无 redirect policy） | 默认配置 |

## 5. 对我们的启示

### 可借鉴

1. **`/reader3/*` API 兼容层**：`router.rs` 约 70 个端点直接对齐 Reader 3.0 Web API（`getBookshelf/saveBook/searchBook/getChapterList/...`）。落地：我们的 `openapi.yaml` 可提供一组 `/compat/reader3/*` 兼容路由，复用现有 Reader 前端与第三方客户端。
2. **`split_top_level` 的 Rust 版实现**（`rule_analyzer.rs:31-86`）：比 Kotlin `RuleAnalyzer` 短 3 倍，且含 `@@` 特判与单测。落地：Go 侧照此写 `SplitTopLevel`，用它的用例（`div[a="x&&y"]&&span`）做黄金测试。
3. **Rust `regex` 换 ReDoS 免疫**：`Cargo.toml` 的 `regex = "1"` 是线性时间引擎。落地：Go 默认 `regexp`（RE2）同理；把「不支持 lookaround/backref」写进契约，超范围规则报 `unsupported_regex`。
4. **`deserialize_rule_option` 的双形态兼容**（`book_source.rs:60-80`）：`ruleSearch` 既接受对象也接受字符串。落地：PG 里 `rules JSONB` 读取时同样兼容「字符串包 JSON」的历史格式。
5. **`jsLib` 书源级共享脚本**（`js.rs:44,321`）：很多真实书源把公共函数放 `jsLib`。落地：goja 侧实现 `source.js_lib` 预编译 + 每书源缓存。
6. **`bookSourceProxy` 端点**（`router.rs:44`）：让书源调试走「服务端代理抓取」，避免浏览器 CORS。落地：`POST /sources/{id}/proxy` 返回原始响应（脱敏后），调试台复用。

### 必须避免

1. **不要让 QuickJS/goja 裸跑**：`js.rs:129`。替代：`goja` + `context.WithTimeout` + `Interrupt` + 输出/内存上限。
2. **不要让 `java.ajax` 绕过网络策略**：`js.rs:188`。替代：所有出网（含 JS 内）走同一个 `fetcher`（含 SSRF 过滤与限流）。
3. **不要把「模式 × 阶段」写成 5×4 份代码**：`rule_engine.rs:92-104`。替代：先解析出 `RulePlan`（mode + steps），再由单一 `Evaluator` 执行，阶段只负责字段映射。
4. **不要把书架塞进单个 JSON 文档**：`json_documents`。替代：`books`/`chapters`/`reading_progress` 三张表 + `chapter_index/offset`。
5. **不要自造密码哈希**：`0003...sql`。替代：`golang.org/x/crypto/bcrypt`/`argon2id`。
6. **不要让「缓存文件 + DB 行」双写无事务**：`file_cache.rs:40`。替代：`StorageProvider` 先写对象存储再写元数据，失败可重试；元数据带 `etag/size`。

### 对 Stage 2 契约的具体约束建议

- `schema.sql`：拒绝 `json_documents` 式书架；`books(id, user_id, source_id, book_url, name, author, ...)`、`chapters(book_id, idx, title, url)`、`reading_progress(book_id, chapter_index, char_offset, updated_at)`；`book_sources.url` 唯一。
- `openapi.yaml`：提供 `/compat/reader3/*` 命名空间（可选启用），核心 API 与兼容 API 分离版本号。
- 沙箱契约：JS 必须「每次执行新建 runtime + 3s 超时 + 内存上限 + 仅白名单 API」；`jsLib` 允许预编译但不得共享可变全局。

## 6. 证据索引

| 文件:行号 | 结论 | 原文短引用（≤2 行） |
|---|---|---|
| `Cargo.toml:24` | 使用 rquickjs 执行 JS | `rquickjs = { version = "0.7", features = ["futures"] }` |
| `Cargo.toml:20` | scraper 做 CSS | `scraper = "0.20"` |
| `Cargo.toml:30` | sxd-xpath 做 XPath | `sxd-xpath = "0.4"` |
| `Cargo.toml:32` | 线性时间正则引擎 | `regex = "1"` |
| `src/parser/rule_engine.rs:37` | `@css:` 显式模式 | `if rule.starts_with("@css:") \|\| rule.starts_with("@CSS:") {` |
| `src/parser/rule_engine.rs:47` | 独有 `@regex:` 前缀 | `if rule.starts_with("@regex:") \|\| rule.starts_with("@Regex:") {` |
| `src/parser/rule_engine.rs:50` | `js:` 无 `@` 也接受 | `if rule.starts_with("js:") \|\| rule.starts_with("@js:") \|\| rule.starts_with("<js>") {` |
| `src/parser/rule_engine.rs:61` | `:` 前缀判为正则（方言差异） | `if rule.starts_with(':') {` |
| `src/parser/rule_engine.rs:67` | 内容像 JSON 即 JSONPath | `if content_trimmed.starts_with('{') \|\| content_trimmed.starts_with('[') {` |
| `src/parser/rule_engine.rs:92` | 五种模式各一条实现 | `ParseMode::JsonPath => {` … `ParseMode::Css => self.search_books_html(...)` |
| `src/parser/rule_engine.rs:113` | 列表为空且有详情兜底 | `if let Some(detail_book) = self.search_detail_fallback(source, body, base_url) {` |
| `src/parser/rule_analyzer.rs:7` | 顶层分隔扫描入口 | `pub fn split_top_level(rule: &str, delimiters: &[&str]) -> SplitResult {` |
| `src/parser/rule_analyzer.rs:79` | `@@` 不作为分隔符 | `&& !(**delimiter == "@" && rule[idx..].starts_with("@@"))` |
| `src/parser/rule_analyzer.rs:94` | 带引号选择器的单测 | `let result = split_top_level(r#"div[a="x&&y"]&&span"#, &["&&", "\|\|", "%%"]);` |
| `src/parser/html.rs:414` | `@` 尾部操作拆分 | `let split = split_top_level(sel_text, &["@"])` |
| `src/parser/html.rs:454` | `%%` 交错合并 | `"%%" => {` |
| `src/parser/html.rs:494` | `@textNodes` 支持 | `let text = get_text_nodes(el);` |
| `src/parser/html.rs:516` | `@html` 支持 | `"html" \| "@html" => Some(el.html()),` |
| `src/parser/js.rs:129` | 每次新建 Runtime（隔离） | `let rt = Runtime::new()?;` |
| `src/parser/js.rs:130` | 每次新建 Context | `let ctx = Context::full(&rt)?;` |
| `src/parser/js.rs:188` | `java.ajax` 直接发请求 | `Func::new(\|spec: String\| -> String { java_ajax(&spec).unwrap_or_default() })` |
| `src/parser/js.rs:321` | `jsLib` 每次 eval 重新执行 | `eval_script(ctx.clone(), &shared_js)?;` |
| `src/model/book_source.rs:19` | 支持 `jsLib` 字段 | `pub js_lib: Option<String>,` |
| `src/model/book_source.rs:43` | 支持 `ruleReview` | `pub rule_review: Option<ReviewRule>,` |
| `src/model/book_source.rs:60` | 规则双形态反序列化 | `fn deserialize_rule_option<'de, D, T>(deserializer: D) -> Result<Option<T>, D::Error>` |
| `src/storage/db/migrations/0001_init.sql:1` | 书源表用 URL 主键 | `CREATE TABLE IF NOT EXISTS book_sources (` / `book_source_url TEXT PRIMARY KEY,` |
| `src/storage/db/migrations/0001_init.sql:14` | 章节缓存存文件路径 | `CREATE TABLE IF NOT EXISTS chapter_cache (` / `file_path TEXT NOT NULL,` |
| `src/storage/db/migrations/0003_users_and_account_documents.sql:25` | 书架等存 JSON 文档 | `CREATE TABLE IF NOT EXISTS json_documents (` |
| `src/storage/db/migrations/0003_users_and_account_documents.sql:2` | 密码 + 盐自实现 | `password TEXT NOT NULL,` / `salt TEXT NOT NULL,` |
| `src/storage/cache/file_cache.rs:44` | 缓存直接写文件 | `fs::write(path, value).await?;` |
| `src/crawler/http_client.rs:12` | 客户端超时来自配置 | `.timeout(Duration::from_secs(timeout_secs))` |
| `src/crawler/http_client.rs:14` | UA 硬编码 Chrome | `.user_agent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) ...")` |
| `src/crawler/url_analyzer.rs:40` | 默认重试 2 次 | `let mut retry = 2usize;` |
| `src/crawler/url_analyzer.rs:70` | URL 选项可覆盖 retry | `if let Some(value) = options.get("retry") {` |
| `src/crawler/fetcher.rs:76` | 重试循环无退避 | `let max_retries = req.retry;` |
| `src/app/config.rs:31` | 默认请求超时 15s | `request_timeout_secs: 15,` |
| `src/api/router.rs:44` | 书源代理端点 | `.route("/reader3/bookSourceProxy", any(handlers::book_source_proxy))` |
| `src/api/router.rs:87` | 兼容 Reader3 书架 API | `.route("/reader3/getBookshelf", get(handlers::get_bookshelf))` |
| `tests/js_compat.rs` | JS 测试仅 14 行 | `js_compat.rs 14` |

## 7. 关键路径文件地图（Stage 2 取证入口）

| 关注点 | 文件（相对仓库根） | 行数 | 用途 |
|---|---|---|---|
| 规则引擎 | `src/parser/rule_engine.rs` | 2,508 | 模式识别 + 5 条求值路径 |
| 顶层切分 | `src/parser/rule_analyzer.rs` | 99 | `split_top_level`（含 `@@` 特判） |
| HTML/CSS | `src/parser/html.rs` | 847 | `@text/@html/@textNodes/@ownText`、`%%` |
| JS 运行时 | `src/parser/js.rs` | 528 | rquickjs、`java.*` 绑定、`jsLib` |
| JSONPath | `src/parser/jsonpath.rs` | 66 | `jsonpath_lib` 封装 |
| URL 编译 | `src/crawler/url_analyzer.rs` | 665 | urlOption、`retry`、`${key}` |
| HTTP | `src/crawler/{http_client,fetcher}.rs` | 25/279 | reqwest、重试循环 |
| 业务 | `src/service/book_service.rs` | 1,717 | 搜索/详情/目录/正文编排 |
| Handler | `src/api/handlers/book.rs` | 3,185 | 所有书籍相关端点 |
| 书源模型 | `src/model/book_source.rs` | 343 | camelCase ↔ snake_case、双形态规则 |
| 路由 | `src/api/router.rs` | 313 | `/reader3/*` 兼容层 |
| 存储 | `src/storage/db/migrations/*.sql` + `cache/file_cache.rs` | 77/91 | SQLite + 文件缓存 |
| 测试 | `tests/book_source_compat.rs` | 552 | 规则兼容回归 |

## 8. 缺陷验证方法（Stage 5 可直接复用）

| 缺陷 | 验证方式 | 期望的「反例」观察 |
|---|---|---|
| JS 死循环 | `@js:while(true){}` | 进程 CPU 打满、无超时 |
| JS 无内存上限 | `@js:let a=[];while(true)a.push(new Array(1e6))` | QuickJS 堆持续增长直至 OOM |
| SSRF | `@js:java.get("http://127.0.0.1:<port>/")` | 能访问本机 |
| 线性正则优势 | 用 `(a+)+$` 型规则跑长串 | 快速失败/快速返回（不回溯） |
| 反向引用丢失 | 用 `(\w+)\1` 规则 | 编译失败（`regex` crate 不支持） |
| 缓存无 TTL | 连续两天读同一章 | 永远命中旧缓存 |
| 密码哈希 | 检查 `users` 行 | 仅 `salt`+`hash`，无 KDF 参数 |

## 9. 对 Stage 2 的开放问题

1. `/reader3/*` 兼容层是否作为我们对外 API 的「别名」，还是仅做导入工具？
2. `regex` crate 的「无反向引用」限制会让多少真实书源失效？需要先采样统计。
3. `Runtime::new()` 每 eval 新建是「安全」但慢；goja 是否可复用 runtime + 每规则新建 `vm`？
4. 书源 `json` 列 + URL 主键是否足够？我们是否需要 `(user_id, url)` 复合唯一（多用户各自一份书源）？
5. 其 `jsLib` 的「每次重新执行」语义（`js.rs:321`）与 Legado 的「共享作用域」不同，我们选哪个？
6. 本地书（TXT/EPUB/PDF/MOBI）在 Rust 侧是强项，我们的 Go 侧是否只做 TXT/EPUB，其余交给 Stage 4 的 Rust 模块？
