# 隔离验证记录（2026-11-12）

本轮新建独立 PostgreSQL 16 集群，仅监听 127.0.0.1:55439，使用 yreader_a1_iso_verify 和 yreader_a1_iso_smoke 两个新库，经目标门禁应用 schema。Redis 为无持久卷临时容器，仅绑定 127.0.0.1:56379。不连接默认 5432，不改动现有持久库，不执行 chapter_cache 物理删除。

## 失败与修复

首次两个 EXPLAIN 测试未选用指定 GIN 索引。测试种子插入后显式 gin_clean_pending_list，再 ANALYZE，消除对 autovacuum 时序的依赖；三项索引测试随后通过，原断言保留。这只证明维护后索引可服务查询，不保证生产所有分布均选择 GIN。

带 Redis 回归暴露取消竞态：取消后迟到的失败收尾覆盖 canceled。JobRepository.MarkFailed/MarkSucceeded 增加 queued/running 条件，保护终态；新增 TestCanceledJobCannotBeOverwritten 覆盖取消后迟到成功及失败。

冒烟首次导入受系统临时目录权限影响；脚本改用项目内临时目录，并明确使用空白冒烟库后通过。

## 最终证据

- go test -tags integration -json ./... -count=1，配置隔离 PG/Redis：171 个顶层测试通过，0 失败、0 测试跳过；含子测试共 237 个通过事件，两者不能相加。
- go vet -tags integration ./... 及后端构建：通过。
- 独立生产配置后端冒烟：healthz/readyz、空库种子管理员登录、expires_in=900、TXT 导入、正文读取、带 If-Match 更新进度、书架返回真实偏移和版本：全部通过。
- 冒烟进程通过 Windows 进程终止清理，此项不作为 Linux SIGTERM 排空验收。
- git diff --check：通过。

本地原始日志保留于忽略目录 .toolchain：verify-integration.jsonl（首次索引失败）、verify-integration-final.jsonl（取消竞态失败）、verify-integration-fixed.jsonl（最终通过）、verify-smoke-server.log。它们不是已提交的 CI 产物。

## 清理与未完成项

已停止临时 PostgreSQL、移除临时 Redis 容器、终止冒烟后端。临时数据和日志保留；Docker Desktop 已启动并保留运行。

前后端 Docker 构建均在解析 docker/dockerfile:1 时因 auth.docker.io/token 连接被远端关闭而失败，尚未进入构建步骤；未验证 Nginx、容器卷权限或完整 Compose 部署，未修改全局镜像源或网络设置。

race 缺 C 工具链及成功流水线证据；完整 OpenAPI/Spectral 校验、旧视图升级演练及前端流程仍待完成。v1.5 保持 Draft，不宣告发布验收通过。


## Xray 代理后的容器复验（2026-11-12）

使用用户现有 HTTP 代理 127.0.0.1:10808，仅向本次 Docker CLI 进程设置 HTTP_PROXY/HTTPS_PROXY，并为镜像构建步骤传入 host.docker.internal:10808；未改动系统或 Docker 全局代理。前后端镜像均已成功构建，补充 .dockerignore 防止复制本机 node_modules、dist 与 Windows 可执行文件。前端构建上下文由首次约 124 MB 降为后续增量约 1 KB。

独立项目 yreader-proxyverify 使用专用网络、tmpfs PostgreSQL 和新文件卷；只向本机开放前端 55173 和后端 55808。新库初始化成功，Nginx 配置检查通过，后端以 UID 10001 运行并可写文件卷。

容器主流程验证通过：SPA 路由刷新、通过 Nginx 登录、token 有效期 900 秒、TXT 导入/正文、带版本进度更新及书架恢复、后端容器重启后正文保留。docker stop 发送 SIGTERM 后退出码为 0，未 OOM，停机命令约 8 秒；该检查为空闲停机，不代表慢请求排空或满负载 Worker 停机压力验收。

在另一个全新临时数据库中应用 Git HEAD 的旧 schema，写入偏移 120 / version 7 的样本，再应用当前 schema 并重复应用一次，视图返回仍为 120 / 7，旧视图升级及重复执行通过。

kin-openapi v0.127.0 完整加载、引用和 Schema 验证通过。Spectral 默认 spectral:oas 规则集为 0 error、40 warning；全部警告为 operation-description（已有 summary，但缺详细 description），未禁用规则。报告位于 .toolchain/spectral-results.json。

前端 npm ci 报告 3 项 moderate 依赖审计提示，未自动升级依赖或执行 audit fix --force；依赖治理不计入本轮构建成功结论。

Linux race 使用独立临时镜像（Go 1.24 Alpine + C 工具链）执行，与生产镜像保持分离；结果在后续段落登记。测试密钥只用于临时环境，未写入源码或打印。原始 race 日志：.toolchain/proxyverify-race.jsonl；容器冒烟结果：.toolchain/proxyverify-smoke.json。


### 最终补交：Linux race、在途请求与清理

Linux 容器内 `CGO_ENABLED=1 go test -race -tags integration -json ./... -count=1` 最终退出码 0：171 个顶层测试通过，包含子测试共 237 个通过事件，0 失败、0 测试跳过、0 DATA RACE；10 个有测试包通过。此为本机 Linux 容器实测，不能冒充 GitHub Actions 已运行的证据。

在途上传排空也通过：先发送一半请求体，触发 SIGTERM；确认新的 HTTP 探测已被拒绝后补齐请求体，原导入请求仍返回 201，进程退出码 0。首次探测误以为 Docker 转发端口的 TCP 握手会随应用监听立即关闭，造成测试超时；修正为 HTTP 拒绝探测后验证通过。首次超时不作为服务缺陷或通过证据。

本轮隔离 Compose 容器、网络、临时文件卷均已删除（先核对配置只引用 yreader_proxyverify_storagedata 与 yreader_proxyverify_default）。临时 PostgreSQL 为 tmpfs，随容器删除；未删除或迁移任何既有数据库。验证镜像与忽略目录中的日志保留，Docker Desktop 保持运行。

剩余边界：Spectral 40 条详细描述警告待补充；旧数据导入 CLI、S3、前端业务及其他发布范围未完成；A2 物理迁移仍未授权，保持独立延期。此次验证通过不代表 Stage 6 全产品封板，三份契约仍为 Draft v1.5，下一步应明确冻结范围及延期清单。
