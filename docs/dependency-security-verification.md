# 依赖安全整改交付（2026-11-13）

依据 dependency-security-design.md 实施；不读取/复制参考源码。Frozen v1.5修订2、43个API操作、数据库版本1与已发布迁移基线不变。

## 版本与范围

| 项 | 旧版 | 最终版本 |
|---|---|---|
| Go最低工具链、CI和生产构建 | go.mod 1.22，本机1.24.2，CI/Docker 1.24 | 1.26.11 |
| golang-jwt/jwt/v5 | 5.2.0 | 5.2.2 |
| antchfx/xpath | 1.3.1 | 1.3.6 |
| go-chi/chi/v5 | 5.2.0 | 5.4.0 |
| jackc/pgx/v5 | 5.7.0 | 5.9.2 |
| klauspost/compress | 1.17.9 | 1.20.2 |
| x/net | 0.26.0 | 0.62.0 |
| x/crypto | 0.24.0 | 0.59.0 |
| x/text / x/sys / x/sync | 0.16.0 / 0.22.0 / 0.7.0 | 0.44.0 / 0.50.0 / 0.25.0（MVS要求） |

不升级没有本轮必要证据的goja、HTML/CSS适配器等，不加入Rust/CGO。前端全锁审计为0，package.json/lock没有为“清零”而改动。JWT HS256、统一401、令牌轮换和XPath节点/文本/属性/组合语义保持。

Go通过官方自动工具链模块获取并校验，缓存位于项目.toolchain/gopath；未关闭GOSUMDB，未替换系统Go或原1.24.2引导器。使用该引导器时应用go.mod要求自动切换1.26.11；离线运行需保留工具链缓存并设置本项目GOPATH。官方ZIP直连/代理下载尝试未成功，未冒充手动归档校验成功。

## 扫描和真实失败证据

1. 升级前官方govulncheck1.1.4 source/symbol扫描使用Go1.24.2，数据库时间2026-11-11T02:51:11Z：113个不同公告ID有版本/包匹配，59个有符号路径；并非113个已复现攻击。原始1260条消息包含971条finding，有同公告多路径重复。记录：`.toolchain/security-go-before.jsonl`。
2. 旧依赖专项实际失败：128KiB畸形JWT分配2,106,280字节；XPath真值布尔表达式在5秒子进程期限内未返回，已杀掉并Wait回收。记录：`security-resource-before.log`。没有向实际服务投递攻击输入。
3. 升级后使用适配新工具链的固定官方govulncheck1.9.0。Windows源码与最终Linux/amd64、Linux/arm64源码扫描均0finding；Linux最终每份289条消息，非空且含配置和OSV元数据。模块级非JSON扫描也通过；最终源码非JSON门退出0。前后扫描器版本不同，不把两个计数当完全相同分析器的精确差分。记录：`security-go-after.jsonl`、`security-go-linux-*-final.jsonl`、`security-go-linux-modules.txt`、`security-go-final.txt`。
4. 最终JWT专项分配1,312字节，XPath父测试约0.11秒完成；子进程先打印进入求值器的标记，避免把启动失败当终止性证据。记录：`security-resource-after.log`。
5. 第一版实际生产镜像扫描仍失败：后端zlib/chi各1项，前端libxslt关联4项及zlib1项。初次Go扫描库尚未报告chi新公告；没有用源码扫描0或条件不可达豁免镜像结果。将chi从中间5.3.0再升到5.4.0，运行层从签名APK仓库更新zlib≥1.4-r0、libxslt≥1.1.43-r4，重建后全部严重度复扫为0。chi公告涉及ClientIPFromXFF，本项目未据此宣称已经发生IP伪造。

扫描工具为Trivy0.76.2，实际容器固定摘要`sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb`，官方发布版本已核验。未使用ignore-unfixed、severity降级、忽略表或跳过依赖包。基础镜像摘要由官方Docker Hub元数据核验并写入Dockerfile：Go1.26.11/Alpine3.24、Alpine3.24.4、Node22/Alpine3.24、Nginx1.30.6/Alpine3.24。

## 最终实际产物（本机linux/amd64）

| 产物 | Docker镜像ID（与Trivy Metadata.ImageID一致） | 扫描 |
|---|---|---|
| yreader-security:backend-local | sha256:5ca411a703a63d7cf0301b2e8ad9c41bbad446b9d6019744b206b555ff6ed563 | OS与Go包0发现 |
| yreader-security:frontend-local | sha256:a28dca288e604c5684ae30d4b04931376e918f24ef305e1f3972720a19e4fd43 | OS包0发现 |

原始完整JSON：`.toolchain/security-reports/backend-final.json`及`frontend-final.json`；首轮失败报告保留为`*-before-fixes.json`。最终包清单确认stdlib1.26.11、chi5.4.0、zlib1.4-r0及libxslt1.1.43-r4。临时镜像标签标记本机验证，不冒充已提交Git版本或已发布镜像。

## 回归

- 普通Go全包测试及最终integration vet通过；新增资源测试先红后绿。
- 最终完整串行集成带独立Redis：196个顶层测试通过，含子测试281个通过事件，0失败；1个辅助worker入口在主进程跳过，已由XPath父测试实际运行。首轮缺Redis的专项后来单独通过，并纳入最终全量。日志：`.toolchain/security-integration-final.jsonl`。
- 前端strict TypeScript、50项单测及生产构建通过。Vite+隔离原生后端11项通过；最终chi版本与生产Nginx镜像+隔离原生后端11项也全部通过（约1.1分钟）。Nginx测试配置仅将API上游指向专用55828后端，不使用业务数据。
- 后端镜像确认非root UID10001、CA存在及Alpine3.24.4；生产Nginx配置与/login SPA回退冒烟通过。
- actionlint1.7.7校验三份工作流通过。修复既有frontend.yml在job.env不允许使用runner.temp的问题，改为运行步骤写入GITHUB_ENV。

## 门禁与局限

新增security.yml：PR/main/手动/每周执行；固定govulncheck1.9.0，分别做Linux两个目标的模块/符号扫描，以及npm全锁审计；四个组件/架构组合构建最终镜像并用固定提交的Trivy Action v0.36.0、固定Trivy0.76.2检查全部严重度。管道保留真实失败退出码与原始报告，不推送镜像。

Makefile新增security-go/security-frontend本地入口。race/frontend工作流从backend/go.mod读取Go版本，避免三处漂移。未新增发布流程、未改变GitHub分支保护设置，不把“有工作流”称为“远程CI已通过”。

本轮没有运行远程Actions或Linux race，未实际构建/扫描arm64镜像；arm64源码扫描通过不等于arm64镜像验收。后续发布前仍需跑配置好的双架构镜像和race门。扫描0仅覆盖本次数据库/产物与支持范围，不能代表未来无新增公告；Go/npm扫描不替代最终OS包扫描。

网络失败均未计作通过：Go官方模块代理直连超时后使用备用代理；Docker Hub直连失败后仅给本轮命令/构建传Xray代理，没有改系统或Docker全局代理。Docker引擎由本轮启动，测试服务清理后保留引擎和本地镜像缓存。

收尾已停止本轮原生后端及临时Nginx/Redis容器，删除随机专用UI库，停止55439验证集群；原5432业务实例不动。10份冻结文件摘要一致，diff检查通过。验证报告、失败证据与镜像缓存保留在忽略目录；未提交、推送或发布。
