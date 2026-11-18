# 备份与恢复契约设计（ADR-019，LocalFS 隔离实验设计 v1）

日期：2026-11-13。状态：Frozen design，范围为 LocalFS 隔离实验 CLI 的设计 v1，冻结记录见 backup-contract-freeze-v1.md；不是工具实现验收、生产迁移或部署批准。43 个 API 操作、数据库版本 1、已发布迁移字节不变。S3 与生产目标仍为独立后续阶段。

## 当前基线

2026-11-13 创建草案前只读核对：本地分支 `codex/pre-release-20261113`，HEAD `fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4`，当时工作区干净；origin 为 `https://github.com/dgulan/YLReader.git`。本草案及后续审计、配置修订尚未提交，不属于该提交的 CI 验证范围。

[PR #1](https://github.com/dgulan/YLReader/pull/1) 为 open/draft，未合并，head 与本地一致，base 为 `main@6ef921e73ecd4f8bcf57304b072e20194b42521f`。连接器查询的该提交实际工作流均为 completed/success：[frontend](https://github.com/dgulan/YLReader/actions/runs/43377355330)、[race](https://github.com/dgulan/YLReader/actions/runs/43377355335)、[security](https://github.com/dgulan/YLReader/actions/runs/43377355329)。这些是已有提交的验收证据，不代表本草案已实现或已验收。

## 1. 首期范围与入口

首期提供**离线整实例灾难恢复包**，通过独立 CLI 管理；服务停写后创建备份，恢复到新建空目标，验证后由运维显式切换。保留用户及实体 UUID，适用于同实例搬迁和故障恢复。跨用户导入、在线热备、增量备份、定时调度、旧版 Reader 导入及合并到已有实例另行设计。

首期不新增 HTTP 端点，不复用 `/jobs`，不把灾备操作放进用户设置页。CLI 拥有实例级数据库与对象存储权限，不能用普通用户 JWT 调用。未来用户级导出必须独立设计租户边界、ID 重映射及 header 解密重加密（AAD 含 user_id/source_id），不能直接复用整实例恢复。

本轮冻结以下命令面（业务实现尚不存在，不应执行）：

| 契约命令 | 行为与必要输入 |
|---|---|
| `backup create` | 显式源配置、目标 StorageProvider 配置、离线证明文件、加密密钥文件；成功返回 backup_id、包摘要、对象数和字节数 |
| `backup verify` | 从 StorageProvider 读取包并解密，检查格式、完整性、版本和密钥覆盖；只读，不恢复 |
| `restore plan` | 显式包、目标配置和 header keyring；只读检查目标为空、权限和容量，输出计划与 plan_digest |
| `restore apply` | 重查同一计划与所有前置条件，显式 `--plan-digest` 和 `--confirm-instance-id`，仅写入专用空目标 |
| `restore status` | 按 operation_id 只读查询阶段、失败码及结果；恢复断线后先查询，禁止自动重跑 apply |

上述确认参数是未来 CLI 的防误操作契约，不是本轮需要用户审批或执行的命令。生产目标规则必须单独评审；不能扩展或绕过现有 migrator 的测试库白名单。连接信息和密钥通过受限配置文件/密钥文件注入，不经命令行明文、日志或输出返回。

## 2. 包内数据与排除项

| 数据 | 首期规则 |
|---|---|
| PostgreSQL | 一致快照的完整应用库，包括用户、密码哈希、权限、设置、书源、书籍、映射、目录、进度、覆盖、书签、替换规则及 schema_migrations；header 保留原加密信封 |
| 会话与任务 | 逻辑数据库快照可包含 auth_sessions、jobs；恢复后在隔离目标撤销全部会话、增加所有用户 token_version，将 queued/running 任务置 canceled，清理 worker 租约；终态历史保留 |
| 文件对象 | 专用应用命名空间内全部已保存对象，包括拆章正文及已保存的封面/字体；按实际 key 清单备份，不凭路径推测正文范围。当前导入不持久保存原始 TXT/EPUB，备份不承诺还原原始上传文件 |
| 外部 URL | 数据库字段原样保留；创建、校验及恢复期间均不访问外站、不执行书源 JS、不下载远程封面 |
| Redis、内存和浏览器缓存 | 不备份。恢复使用新的 Redis 命名空间或独立实例；客户端切换后清理离线缓存，避免同 UUID 下旧正文污染 |
| 环境与密钥 | 不打包 .env、JWT 签名密钥、header 主密钥、S3 凭据、DSN、日志及 references；manifest 仅记录需要的 header kid |

完整数据库含敏感数据，不能当作脱敏导出分享。恢复要求外部托管的 header keyring 覆盖快照内所有 kid；缺少任何所需密钥或抽检解密失败都拒绝 apply。JWT 签名密钥必须重新生成，旧 token 不能在恢复实例继续使用。备份加密密钥与 header 密钥分开托管；丢失任一必要密钥不承诺恢复。

审计补记（2026-11-13）：上传文件在 Handler 中临时接收并于请求结束删除，Service 只持久保存拆章正文及可用封面；file_hash 不代表原始文件仍在。若后续要求下载或逐字节恢复原始 TXT/EPUB，须先独立设计原文件留存与配额，不能由本备份契约推定已有该能力。

当前格式只支持权威版本 1 的 14 张应用表。如源仍有历史 `chapter_cache` 或额外应用表，创建前返回 schema_incompatible，另行设计兼容路径；不静默漏备份、不自动删除，也不执行 A2。恢复后本地书必须无需回源即可读；在线正文缓存允许失效。

## 3. 一致性与 StorageProvider 边界

备份创建前停止全部 API 写入、Worker、巡检、导入和所有实例的其他写进程，等待在途事务与对象写入排空；对象命名空间禁止外部写入。源库可以只读。单个 CLI advisory lock 不能证明应用已停写，离线证明须由运维记录全部写入者停机及对象独占条件；不能证明时拒绝创建。

进入静止点后，在一致数据库快照内导出库，遍历专用对象命名空间并流式复制、计算摘要；结束前再次核对对象清单与摘要。数据库引用的必需本地对象缺失，或对象集合发生变化，整个创建失败。静止窗口覆盖数据库导出及全部对象复制，不能只在导出瞬间停写。RPO 是 manifest 的 snapshot_at，RTO 由真实恢复演练测量，本草案不承诺固定恢复时长。

对象源、备份目标、恢复目标均走 StorageProvider；LocalFS/S3 使用同一语义。现有接口能力不足时，先设计流式枚举、统计、分段读写和不可覆盖发布能力，再冻结实现契约。禁止业务层拼接宿主绝对路径，禁止以本地目录复制代替对象契约；S3 驱动未完成前不得声称已验收双驱动。

备份目标须位于源命名空间之外，避免递归备份；备份对象只能以新 backup_id 写入。临时对象不被列表视为成功包；全部验证完成后最后发布完成标记。校验失败保留安全诊断并标记 failed，部分包不可恢复；临时数据清理只能针对本次 operation 的专用前缀，不能递归清理用户目录或共享 bucket。

## 4. 包格式与安全校验

逻辑格式标识 `ylreader-instance-backup`，format_version 为整数 1。加密封装内包含 `manifest.json`、`database.dump`（PostgreSQL custom archive）及 `objects/<opaque-id>`。原对象 key 存在 manifest 中，不能直接作为归档文件名。manifest 至少包含：

| 字段 | 类型与约束 |
|---|---|
| format / format_version | 上述固定值；未知版本拒绝 |
| backup_id / source_instance_id | UUID；instance_id 由外部实例配置提供，不以数据库名推断 |
| created_at / snapshot_at | RFC 3339 UTC；snapshot_at 为确认静止点时间 |
| app_revision / schema_version / migrations | 字符串提交、整数版本、按版本排序的 name/checksum 账本；不得只信版本号 |
| postgres_major / required_header_kids | 首期 PostgreSQL 16，kid 去重排序数组 |
| database | 归档名、int64 字节数、64 位小写十六进制 SHA-256 |
| objects | 每项包含 opaque-id、原 key、int64 字节数、SHA-256；key 与 opaque-id 均唯一 |
| counts / total_plaintext_bytes | 各权威表行数、对象数、总解密字节数，用于复核；不替代恢复后的关系检查 |

数据库归档和对象摘要覆盖完整字节，manifest 本身受认证加密保护。包 SHA-256 供定位与传输复核，不是可信发布者签名；不接受来源不可信的数据库归档。恢复器须在隔离环境审查并拒绝超出应用 allowlist 的角色、扩展、函数及所有权操作，不以数据库超级用户执行包内 SQL。

封装候选确定为 age v1、X25519 recipient，采用官方 Go 实现；详见第 8 节。首期不接受用户口令直接作为密钥，不自行设计分块 AEAD。必须完整读取认证流并通过末尾校验，才允许使用数据库内容。

解包逐项限制路径：拒绝绝对路径、盘符、UNC、`..`、反斜杠、NUL、软/硬链接、设备文件、重复条目、未登记条目和越界大小。严格比对解密后总字节数与条目数；流式读取限制内存。首期默认包上限 100 GiB、解密总量 200 GiB、对象 1,000,000 个、单对象 10 GiB；可通过显式受校验配置降低或提高，但实际磁盘/配额不足仍拒绝。大清单实现必须流式处理。

## 5. 恢复状态机与失败边界

`planned -> staging -> validating -> ready_for_cutover -> completed`；前四阶段可进入 failed/canceled。completed 仅表示隔离目标验证完成并生成交接报告，不代表流量已切换；实际切换在命令外另行授权执行。取消只在提交检查点响应，不承诺强杀外部数据库工具；状态记录区分 cancel_requested 与实际 canceled。

1. plan 校验目标为新建空数据库和空的独占对象前缀，目标不是源实例、不是源对象或备份前缀；运行中应用连接数必须为零。输出包摘要、目标标识、预计容量、动作列表和计划有效期（15 分钟）。plan_digest 覆盖这些内容及格式/账本版本。
2. apply 以 operation_id 持久记录阶段并锁住目标，重新核对全部前置条件、计划时效及摘要；同目标并发 apply 拒绝。目标隔离依赖部署侧停止应用及独占凭据，不能只凭咨询锁防止第三方写入。
3. 在专用对象前缀先暂存并验证全部对象，再在新空库使用受信基线建立 schema，按固定依赖顺序恢复归档数据与 identity 序列。使用 `--no-owner` / `--no-acl` / `--exit-on-error` / `--single-transaction`，不禁用外键触发器，授权由目标配置管理。归档须是完整校验后的可寻址暂存文件。只接受 schema_version=1 且账本校验和与当前内嵌迁移完全匹配；不自动 up/down、不修改历史基线。基线初始化的账本种子只能在该新建隔离目标的数据加载事务内替换，结束后必须与预先验证的归档账本及可信基线完全匹配。
4. 校验对象全量摘要、必需引用、外键、表行数及进度/目录关系；保留实体 UUID、chapter_index、char_offset、updated_at 和 version，不调用多端进度合并。之后执行会话撤销及任务终止，报告这些预期行数变化。
5. 校验目标 header 可解密，使用新 JWT 密钥和隔离缓存，输出 ready_for_cutover 报告。完成交接报告后记录 completed。源实例始终不被覆盖或删除。

DB 与对象存储无跨系统事务，不承诺同时回滚。失败留下的隔离目标不可启动服务；operation 报告指出残留位置，清理仅允许显式指定该 operation 创建的目标。首期失败不支持断点续跑，需要创建另一空目标重新 plan/apply。若客户端退出而结果未知，先查 status；同 operation_id 重复提交相同 apply 只返回已有状态，参数不同返回 conflict。状态持久保存于目标操作控制区，通过 StorageProvider 写入且与业务对象前缀分离；不得只放 Redis。

切换需独立检查 readyz、登录、本地书读取、在线书目录、进度和书签；验证之前不得开放目标对外服务。切换失败时停止目标写入并回到保留的源实例；若目标已接收新写入，不承诺无损回切，必须先保存目标数据并人工处理差异。源与目标不可同时接受同一实例的写入。

## 6. CLI 输出与错误契约

`--output json` 输出单个 UTF-8 JSON 结果；进度写 stderr，不含凭据或书源 header。结果必含 operation_id、status；成功附 backup_id/package_sha256 或 plan_digest/target_instance_id/report。失败为 `{error:{code,message,details},operation_id,status}`；客户端按 code 判断，不依赖 message。status 查询不存在的 operation 返回 not_found。

| 退出码 | 稳定 code 示例与含义 |
|---|---|
| 0 | 操作完成或 status 查询成功；查询返回 failed 仍以 status 字段表达历史失败 |
| 2 | validation_error、unsupported_backup_format、limit_exceeded：输入/格式/预算拒绝 |
| 3 | target_not_empty、source_not_quiescent、plan_expired、conflict：前置条件不满足 |
| 4 | integrity_failed、missing_header_key、decrypt_failed、schema_incompatible：完整性或兼容性拒绝 |
| 5 | storage_failed、database_failed、internal_error：执行失败，先查 operation 状态 |
| 130 | canceled：已经确认取消；单纯连接中断不能伪报取消 |

认证与存储权限失败脱敏返回，不打印 DSN、对象签名 URL、密钥或归档 SQL。首期不支持从任意 HTTP URL 拉包，只接受已配置 StorageProvider 中显式对象标识。

## 7. 实现与验收门禁

本轮根据数据库、加密及 LocalFS 原型冻结第 8–9 节的协议和目标边界。ADR-019 纳入 architecture；OpenAPI 不增操作，schema 不变，无需数据库迁移。容量数值冻结为输入保护上限，不是承诺可在所有机器处理到上限；实际容量、性能、端到端数据恢复和故障注入在实现阶段验收。不能为“达到预算”而取消实际空间、权限或目标门禁。

实现验收至少覆盖：

- LocalFS 与 S3 分别完成创建、verify、空目标恢复和读取闭环；跨驱动恢复。
- 多用户/本地 TXT、EPUB/书源加密 header/映射/覆盖/书签/偏好/精确进度全量对照，旧 JWT 与 refresh token 均失效，在途任务不重放。
- header 缺 kid、错误密钥、篡改包、截断、缺对象、未知 schema/账本漂移、目标非空、源目标重合均在切换前拒绝。
- 恶意路径、重复 key、链接条目、超量清单与解压膨胀、超出 allowlist 的数据库归档拒绝；不联网执行源规则。
- 复制中断、恢复中断、磁盘满、S3 拒绝写入、进程崩溃、并发 apply、重复 operation、计划过期及状态查询，验证源保持完整、残留可定位且不自动清理共享资源。
- 静止条件不满足时拒绝；默认量级和大书恢复记录真实耗时、峰值内存、对象数与字节数；既有安全/race/生成契约检查继续通过。

演练只使用专用隔离数据库和对象前缀，不连接或清空现有持久实例。本轮未创建真实备份、恢复数据、修改生产配置、推送、合并 PR 或部署。

## 8. 冻结前细化：封装、存储和操作控制区

以下决策作为 LocalFS 隔离实验设计 v1 冻结，架构记录为 Frozen v1.5 修订 3。LocalFS 为第一实现子阶段；完整交付仍要求 S3 及双向跨驱动验收，不因子阶段通过而取消宪章要求。

### 8.1 加密与容器格式

采用 [age v1 规范](https://age-encryption.org/v1) 的 X25519 recipient 模式，通过官方 `filippo.io/age` Go 实现生成 identity；创建只需公开 recipient，verify/restore 读取独立保管的私钥 identity 文件。首期只允许一个 X25519 recipient，不允许插件、SSH recipient、口令或交互提示。依赖的具体固定版本、漏洞核验及官方互操作样本必须在冻结前登记，不能以浮动 latest 作为生产依赖。

原型已固定 `filippo.io/age v1.3.2`，独立模块锁定 `x/crypto v0.59.0`、`x/sys v0.50.0`、`x/term v0.48.0`，通过官方 CLI/库互操作、上游测试向量及最终 CLI 二进制漏洞扫描；详见 [backup-envelope-prototype-2026-11-13.md](backup-envelope-prototype-2026-11-13.md)。Go 模块校验保持启用，业务 backend/go.mod 未改。

内层采用未压缩 POSIX tar：manifest.json 为第一项，之后为 database.dump、按 opaque-id 排序的对象常规文件；禁止链接、目录及 PAX/GNU 扩展条目。包后缀 `.tar.age`，外层完成标记只含 backup_id、密文字节数和 SHA-256，不含用户、书名或 header kid。manifest 大小默认最多 256 MiB，并与总对象预算一起校验。

age 为流式认证；前段可以成功解密不代表完整包有效。因此 verify 和 restore 必须完整读取密文、检查结束及所有摘要，将解密内容仅放在权限受限的专用暂存区；认证完成前绝不向目标数据库或业务对象前缀写入。中途失败不产生成功标记。暂存明文目录须由驱动创建为仅操作用户可访问，Windows 对应限制 ACL，不能只依赖 POSIX mode；权限无法验证就拒绝。退出清理失败要报告专用位置，不能承诺物理安全擦除。

### 8.2 存储扩展的最小语义

不扩大所有业务调用者的 Provider 要求；备份工具通过独立能力接口组合现有 Provider：

| 能力 | 契约 |
|---|---|
| List(ctx, prefix, cursor, limit) | 分页返回完整 key、size 和下一 cursor，稳定字节序、不重复；静止期间遍历必须完整；单页最多 1000 项。遍历不持有全量对象内容 |
| Stat(ctx, key) | 返回 size 及可选版本 token；token 只能检测变化，S3 ETag 不能替代 SHA-256 |
| CreateOnly(ctx, key, reader, size) | key 已存在时 conflict；并发调用至多一个成功；不能用 Exists 后 Put 模拟原子性 |
| CompareAndSwap(ctx, key, expectedToken, bytes) | 控制区状态按版本条件替换；版本变化返回 conflict，失败保留旧完整版本 |
| RestrictedTemp(ctx) | 建立隔离明文暂存区并返回清理句柄；底层本地路径只在驱动/外部工具适配层使用，不进入业务对象 key |

LocalFS 的完成标记及状态发布需要同步文件、同卷原子发布和必要的目录持久化；S3 必须使用服务端条件写并测试目标兼容实现，不具备条件写时拒绝该后端。大包首期顺序流式读写，无分段续传需求，不提前增加 Range/Multipart 公共能力；分片仅可作为 S3 驱动内部实现。清理操作必须验证 namespace 与 operation 所有权，拒绝空前缀。

### 8.3 实例标识、配置与计划摘要

instance_id 为安装时生成并独立保存的 UUID；恢复目标使用新 UUID，源 UUID 仅用于溯源。控制区由独立 StorageProvider 配置指定，布局为 `operations/<operation_id>/`，必须与源、备份和恢复业务对象命名空间均不重合；新实例启动是否允许服务由外部部署门禁决定，不误称现有应用已经识别恢复标记。

配置文件严格拒绝未知字段、重复键及目标相关环境覆盖；DSN/identity 通过受限文件加载。配置结构区分 source、backup、target、control、temporary、limits，所有目标显式填写，不能默认选用当前应用 DATABASE_URL。首期写入能力仅支持回环地址与新建隔离数据库白名单，具体限制至少与现有 migrator 一样严格；生产入口另行评审，不能复用测试例外。

计划结构以 backup-contract.schema.json 的 plan 为准。移除 plan_digest 后递归按字段名 ASCII 升序序列化：UTF-8、无 BOM/空白/末尾换行，整数十进制，布尔值小写，不允许浮点数；计划中全部键和值限 ASCII，字符串引号、反斜杠及控制字符使用 JSON 标准转义。数组保留顺序。plan_digest 是上述字节的 SHA-256；示例固定摘要供跨语言对照。DSN 密码不进计划；可审阅的 target_database 为解析后的 host/port/database，存储与控制区等物理身份只存 SHA-256 指纹。apply 重算摘要、重新鉴权、重解析真实目标并比较指纹，摘要不能替代访问授权。

每次状态转换使用 CAS 更新 generation，记录 stage、started_at、heartbeat_at、finished_at 和脱敏 failure_code。进程锁释放或心跳丢失不能直接判定 canceled：status 返回最后持久阶段及 stale=true，恢复管理命令确认原进程退出后才将其终结为 failed/operation_interrupted。不得根据超时自动重放数据库恢复。同一 operation_id 的重复 apply 在任何阶段都只读返回既有状态；不同计划使用相同 ID 返回 conflict。

### 8.4 数据库归档安全约束与原型依据

[PostgreSQL 16 官方说明](https://www.postgresql.org/docs/16/app-pgrestore.html)明确指出恢复归档可能执行源端选择的代码；`--no-owner`、`--no-acl`、TOC 对象名白名单都不能证明 SQL 安全。不能把“只恢复允许的表名”当作沙箱。

首期仅接受本工具从受信源创建的包，且目标必须是无外网、无业务凭据的独立隔离集群；不得将不可信归档连接到共享业务 PostgreSQL。冻结前必须用真实 schema 的 pg_dump/pg_restore 原型确认 pgcrypto/pg_trgm 扩展的预配置、应用角色权限、触发器及外键顺序，明确哪些归档条目由可信基线提供、哪些可恢复；不通过时改为可信基线建库与结构化数据导入方案。该原型只允许隔离测试，不自动获得生产目标授权。

原型补记（2026-11-13）：已完成 20 项隔离数据库检查，详见 [backup-db-prototype-2026-11-13.md](backup-db-prototype-2026-11-13.md)。非超级用户的数据库所有者可安装本镜像中的受信 pgcrypto/pg_trgm 扩展并恢复完整 schema；采用可信基线加数据恢复也可保留所有样本字段，但必须按固定依赖顺序加载，并使用可寻址归档文件。直接按归档默认顺序 data-only 恢复会因 app_settings 先于 users 违反外键；对 stdin 重排会因不能 seek 失败。这些失败均保留原始证据。

候选采用可信基线提供全部 DDL/扩展/函数/约束/视图/触发器，只允许归档中的 14 个已知表 TABLE DATA 与 2 个已知 identity 序列 SEQUENCE SET 项；表集合、列形状及账本应先校验，未知条目拒绝。固定顺序为 users → book_sources → books → book_source_mappings → chapters → reading_progress → chapter_source_overrides → bookmarks → replace_rules → app_settings → auth_sessions → source_failures → jobs → schema_migrations，之后设置 identity 序列。不使用归档中的 DDL、权限或角色定义。该选择减少归档执行面，但仍不构成不可信输入沙箱，来源信任及隔离目标要求不变。

本次原型只验证数据库行为，未验证真正加密 header 的解密、对象文件读取或应用登录；不能因此宣称完整备份恢复已通过。原型清除账本种子发生在全新临时目标；正式实现须将其与数据加载纳入同一事务，不照搬实验脚本的分步写入。

第 7 节中的容量数字是保护上限，不是实测吞吐或最大容量能力。原型证据、依赖固定和冻结范围登记于 backup-contract-freeze-v1.md；业务恢复实现必须继续完成第 7 节的验收门禁。

### 8.5 机器可校验结构与边界

[backup-contract.schema.json](backup-contract.schema.json) 为 JSON Schema Draft 2020-12；根以 oneOf 区分 manifest、LocalFS 实验配置、恢复计划与持久操作状态。四份示例位于 docs/examples，仅说明结构；包摘要为合成占位，计划摘要为按固定序列化规则计算的真实样例值，不是可恢复包或可用配置。实验配置固定 config_format=ylreader-backup-localfs-experiment；不接受 S3，不代表正式取消 S3。

JSON Schema 拒绝未知字段、非法格式/版本、负大小、越界值、路径控制字符和多余表计数。配置只接受 DSN/密钥的绝对文件路径引用，不能内嵌密码或连接串。命令级检查另行落实 create 需要 recipient、verify/restore 需要 identity，schema 本身至少要求其中一个引用。

脚本 scripts/verify-backup-contract.py 验证结构并演示必要的跨字段规则：严格 JSON 解析拒绝重复键和 NaN/Infinity，UTC 实际日期及先后关系、对象 ID/key 唯一、对象数一致、kid 排序、总明文字节等于数据库加对象字节数（不含 manifest/tar 开销），源目标 instance_id 不同及四存储区/暂存区无重叠；连同计划和状态模型共有 65 项样本检查通过。

这些都是离线结构检查，不读取引用文件，不校验 DSN 的实际解析目标、真实磁盘容量、ACL、符号链接/挂载别名、归档对象摘要或数据库权限。未来执行器必须在所有写入前通过驱动解析物理位置、拒绝别名重合并执行真实目标门禁，不能凭本 schema 或字符串路径比较签发恢复许可。

## 9. LocalFS、恢复计划与状态的最终约束

### 9.1 文件系统边界

暂存根必须由操作用户独占写入，不能复用已有目录而假定其安全。Windows 在创建时指定 protected DACL 和当前用户 owner；空文件同样显式指定 owner/DACL，CreateNew 并禁止共享，在验证后才写明文。目录私有不代表新文件 owner 正确，不能仅靠继承。Linux 使用非 root 用户、目录 0700、文件 0600 和排他创建；拒绝组/其他用户权限。存在额外 ACL 授权或无法读取安全属性的文件系统应拒绝，不能只看 mode 宣称安全。

拒绝对象和祖先中的 reparse point/符号链接；常规文件 link count 必须为 1。源、目标、备份、控制区、暂存区必须物理隔离；检查设备/文件身份及祖先关系，不能仅比较字符串。目录联接、硬链接、大小写或其他物理别名均不能绕过。若驱动无法稳定确定位置，返回 validation_error。

正式实现必须用已验证的目录/文件句柄执行后续操作，拒绝检查后被替换的路径，不跟随意外链接；原型的路径扫描只用于验证拒绝规则，不是 TOCTOU 防护实现。由同一操作用户或管理员主动修改权限/夺取所有权的情况不纳入非特权隔离保证。任何跨平台目录同步或 ACL 能力不确定时拒绝发布成功标记，不能降级为“尽力而为”。

Windows PowerShell 7.6.9 上 12 项、Linux 非 root 容器上 9 项静态权限与别名实验通过。PowerShell 5.1 因本机执行策略未能运行，不承诺该运行时支持；正式 Go CLI 的平台能力必须按各平台实现测试单独验收。

### 9.2 计划有效性和存储身份

计划生存期严格为 created_at 后 900 秒；apply 要求 created_at ≤ now < expires_at，边界过期，不能自动延长。计划绑定 operation_id、backup_id、包/manifest 摘要、基线校验和、源目标实例、回环测试数据库、三个不同的位置指纹、预算和固定步骤。确认实例必须精确等于 target_instance_id。

位置指纹由驱动解析后的 provider、文件系统/设备身份、实际目录身份和命名空间组成固定格式后计算，不能使用带密码的配置。不存在的新目标须先在独占父目录内预留操作自己的空目录并取得身份，再生成计划；该预留不写入业务数据。计划不等于创建/覆盖数据库的授权。apply 重新核对同一身份、目标为空、空间和全部访问权限；任何漂移要求重新 plan。

### 9.3 状态转换和取消

operation 文件是 schema 中的持久状态，不包含动态 stale 标识。generation 从 1 开始，每次 CAS 更新精确加 1；必须比对 expected_generation，冲突方重新读取，不能覆盖。operation_id、backup_id、plan_digest、created_at 和 owner_id 绑定后不可变；updated_at 严格前进，heartbeat 不倒退。终态必须有 finished_at 且等于 updated_at；只有 failed 带非空 failure_code。

主线为 planned → staging → validating → ready_for_cutover → completed；可以保持当前阶段更新心跳或取消标记，不能跨阶段跳转。前四阶段均可转 failed/canceled，所有终态不可再写。cancel_requested 一旦为 true 不得清零，之后只允许当前阶段检查点记录或进入 canceled/failed；canceled 表示执行器已确认停下，不能由请求端自行写成。completed 只是隔离目标验证和交接报告完成，不代表切换生产流量。

运行期每 10 秒更新心跳，status 对非终态在距最后心跳超过 60 秒时额外返回 stale=true，终态 stale=false；这不是接管租约或死亡证明。只有确认原进程退出且没有遗留数据库子进程后，独占控制锁持有者才可 CAS 到 failed/operation_interrupted，不恢复执行。新计划使用新 operation_id 和新空目标。进程锁由 OS 随进程退出释放，控制记录仍通过 Provider 保存；不能靠删除锁文件或按年龄强抢锁。

计划和状态测试为纯模型，未验证文件系统 CAS 的并发和崩溃持久性；这些属于实现必须通过的故障测试。CreateOnly、CAS、目录同步、句柄安全和完整数据恢复未交付前，不得把本设计冻结称为工具可用。
