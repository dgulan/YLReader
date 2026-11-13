# OpenReader 更新对照审查（2026-11-13）

## 范围与方法

- 本地只读参考仓库：`D:\YReader\references\changshengyu_openreader`，HEAD 为 `9076cfad18d6a14d96c58724ecb65fe61c9c818c`（2026-11-04）。没有 fetch/pull、修改参考仓库或全局 Git 配置。
- 通过 GitHub 连接器核对线上 main：`f82e0f5a8a4acf2a5492cf8c88d21548b3c44536`（2026-11-12 10:06:36 UTC）。比较结果为 ahead 46 个提交、56 个变更文件，非“46个功能”。[固定比较链接](https://github.com/changshengyu/openreader/compare/9076cfad18d6a14d96c58724ecb65fe61c9c818c...f82e0f5a8a4acf2a5492cf8c88d21548b3c44536)。
- 参考侧仅审查提交元数据、变更文件清单、维护者的合同/审计文档，不分析、复制或引入参考业务源码。本会话没有 REA 工具，故不冒充重新完成 Stage 1 源码逆向。原有冻结研究报告不修改，本文件是增量记录。
- YReader 侧为只读源码、配置及实际模块版本检查，`go list -m` 确认选中版本；没有升级依赖、改业务实现、启动服务、迁移数据库或执行攻击复现。本轮也没有执行完整 govulncheck、容器扫描或浏览器测试。
- 官方公告区分“受影响版本且入口使用中”和“在部署环境中已复现”。以下 P1 是建议处置优先级，不等于已证实生产被利用。

## 最近46个提交的主要变化与适用性

| OpenReader 更新 | YReader 对照 | 结论 |
|---|---|---|
| Go 1.26.11、安全公告门阻止旧工具链发布 | 本机实际 Go 1.24.2；Docker 构建为 `golang:1.24-alpine`；两份 CI 为 1.24；go.mod 声明1.22 | P1：维护受支持工具链，扫描后统一精确补丁版本；模块语言声明不代表实际编译器版本 |
| 发布期 zlib、CA bundle 更新，未知来源 OS 包复审到期机制 | 后端运行镜像 `alpine:3.20`；前端 `nginx:1.27-alpine`；没有实际产物 OS 公告门 | P1：更新镜像支持基线；需检查真实包清单，不能把参考项目的 zlib CVE 直接认定为本项目同版本漏洞 |
| 注册邀请码、用户数量上限 | 当前43个API操作没有公开注册，账户由种子流程创建 | 当前不适用；将来实现多用户注册时，先定义默认关闭/邀请码、事务内上限和防枚举行为 |
| RSS `(user_id,url)` 与 `(user_id,custom_order)` 非唯一索引 | 当前没有 RSS 子系统，主要书源/书籍已有面向查询的 PostgreSQL 索引 | 不移植RSS索引；借鉴真实查询计划与大用户数据回归方法 |
| 退役书源/RSS按用户进程锁，避免恢复事务和保存请求互等 | 当前使用 PostgreSQL/pgx，没有相同SQLite恢复与进程锁组合 | 未发现相同根因；未来备份恢复须审计“数据库事务→进程锁”与反向顺序，不能简单删除现有任务互斥或咨询锁 |
| 登录卡片在360/390窄屏溢出的修复 | YReader样式已有全局border-box、移动端单列及width/max-width；前端组件不同 | 无证据认定同一布局缺陷；值得补登录页360/390宽度和长错误文案断言，本轮未跑浏览器确认 |

最近发布记录称镜像和 Actions 已完成，但设备仍待验证。这是上游维护者声明，本轮没有独立下载镜像或重跑其CI。[发布记录](https://github.com/changshengyu/openreader/commit/f82e0f5a8a4acf2a5492cf8c88d21548b3c44536)。

## 确认值得优先修复的共用依赖问题

上游较早的依赖审计记录也被此次更新继续引用。以下JWT/XPath问题不是最近46个提交才引入或首次修复；它们是本次对照中发现YReader仍未处理的存量缺口。[上游依赖审计](https://github.com/changshengyu/openreader/blob/f82e0f5a8a4acf2a5492cf8c88d21548b3c44536/docs/compat/runtime-dependency-advisory-fixed-baseline-second-audit-p2-contract.md)。

### 1. P1：JWT畸形输入过量分配

`backend/go.mod` 及实际模块选择均为 `github.com/golang-jwt/jwt/v5 v5.2.0`。`backend/internal/api/auth.go` 将Bearer内容交给 `AuthService.Authenticate`；`backend/internal/service/auth.go:347` 调用 `jwt.ParseWithClaims`，未在JWT解析前限制令牌长度/结构。官方 [GO-2025-3553](https://pkg.go.dev/vuln/GO-2025-3553) 影响5.2.2之前的v5版本，涉及头部解析中的过量内存分配。

结论：受影响版本和使用入口已确认；HTTP默认头大小限制能限制单次输入，但不能替代依赖修复。建议选择经当前公告扫描确认的兼容v5补丁，至少包含5.2.2修复；补充有界异常输入分配测试，保持正常HS256、通用401及刷新轮换行为。不能将5.2.2直接称为2026-11的完整安全基线。

### 2. P1：XPath布尔表达式可能不终止

实际间接依赖为 `github.com/antchfx/xpath v1.3.1`，由htmlquery使用。`backend/internal/engine/parser/xpath.go:31` 和 `list.go:202` 调用 `htmlquery.Find`；这两个函数不接收可中断context，调用方即使有超时也不能据此中止底层循环。官方 [GO-2026-4526](https://pkg.go.dev/vuln/GO-2026-4526) 说明1.3.6之前版本在特定真值布尔XPath下可无限循环。

结论：受影响依赖与节点遍历入口存在，静态证据充分支持优先升级；具体本项目表达式复现本轮未执行。JS沙箱超时、抓取超时和响应字节上限均不能代替XPath底层终止性修复。回归应将异常XPath放入可超时杀掉且Wait回收的独立测试子进程，避免主测试进程留下满CPU协程；同时覆盖正常节点、文本、属性和组合规则。至少包含1.3.6修复，但最终版本还需当前扫描核验。

### 3. P1：Go工具链、HTML依赖与镜像维护基线

- Go官方目前列出1.26/1.27分支，发布政策只维护最近两个大版本；本机1.24.2、CI/Docker的1.24均应调整。可评估1.26.11或更新的受支持安全补丁，先验证依赖最低Go要求，再同步本机/CI/Docker。[Go发布政策及记录](https://go.dev/doc/devel/release)。
- `golang.org/x/net v0.26.0` 被HTML解析路径使用。官方 [GO-2025-3595](https://pkg.go.dev/vuln/GO-2025-3595) 涉及0.38.0之前HTML外来内容中的DOM作用域解析错误。当前正文以文本渲染，不能直接宣称本项目存在同源XSS；但解析结果正确性与依赖修复仍应回归。此公告仅是已确认示例，不是完整x/net扫描结果。
- `golang.org/x/crypto v0.24.0`、`x/text v0.16.0` 也应纳入完整调用可达性扫描，不能因为模块陈旧就认定所有子包漏洞可利用。当前密码路径使用argon2，不是SSH/OpenPGP。
- Alpine3.20常规支持于2026-04-01结束，现为按请求维护；后端应选择常规支持中的分支。前端nginx镜像与实际CA、openssl、zlib等包同样需核验，不用Windows本机检查冒充Linux最终镜像结论。[Alpine支持表](https://www.alpinelinux.org/releases/)。

上游最新 [Go 1.26.11合同](https://github.com/changshengyu/openreader/blob/f82e0f5a8a4acf2a5492cf8c88d21548b3c44536/docs/compat/dependency-advisory-go-1.26.11-maxbytesreader-p1-contract.md) 报告GO-2026-6859与MaxBytesReader相关。本轮官方公告详情地址未能成功读取，故具体竞争条件仅记为上游声明，未独立确认。YReader也使用MaxBytesReader，但当前HTTP writer经过Logging包装，且读取后再写响应；不能仅凭API同名就认定触发相同崩溃，亦不据此豁免旧工具链维护。

## 不应误报为本项目同类缺陷的部分

- **每请求会话续期写入**：上游本地基线已经包含该节流修复。YReader `AuthService.Authenticate` 只SELECT token_version，last_login_at只在登录写入，没有相同的每鉴权请求UPDATE；不需要再加同样节流层。
- **公开默认JWT密钥与旧会话收编**：上游历史P0依赖旧格式收编窗。YReader没有该兼容分支，Compose明确生产环境并要求JWT_SECRET，不能照搬“相同P0”结论。不过手动运行若未设production且未提供密钥，仍会使用公开开发回退值；生产启动配置应更明确地限制开发回退和已知弱值。不能将“没有旧收编窗”等同于“公开密钥安全”。
- **SQLite写锁/BEGIN IMMEDIATE**：本项目是PostgreSQL，无SQLite DSN、WAL和busy_timeout。迁移这些选项既无效也不符合技术栈。

## 建议后续开发顺序

1. 先做依赖安全专项：完整Go可达性扫描及前端全锁扫描，保留原始结果；区分版本匹配、符号路径与实际部署条件。漏洞、扫描失败或不完整结果不能当绿灯。
2. 选择受支持Go补丁及最小必要兼容依赖升级，优先修JWT/XPath，回归解析、认证、抓取/SSRF、TXT/EPUB和全部既有集成场景。保持Frozen v1.5行为；若最低工具链或安全策略需调整，先写设计说明。
3. 建立Go/npm和最终容器OS门禁；固定可追溯镜像、记录revision和多架构产物证据，豁免必须逐项说明且有期限。不要照抄上游具体apk包版本或包来源豁免。
4. 再推进备份恢复；先定义事务与文件补偿、锁序、跨用户隔离及历史备份夹具，吸取上游恢复互锁的经验。公开注册和RSS属于未来产品范围，不为“跟上游”而提前加接口。

本轮仅新增本研究记录。现有冻结研究、architecture/schema/openapi、业务源码及依赖锁文件保持原样。
