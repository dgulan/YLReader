# 发布前验证（2026-11-13）

范围：用户批准“整理提交、远程CI、Linux race、arm64实际镜像构建与扫描”。不是发布批准；未部署、合并或推送生产镜像。用户后续指定目标仓库dgulan/YLReader，并要求同步项目名称和本地目录。

## 可审阅提交

独立分支：`codex/pre-release-20261113`，main保持`37380a2`。既有授权工作按依赖整理为：

| 提交 | 范围 |
|---|---|
| 7b1e098 | Frozen v1.5契约、设计、阶段验证记录、文档换行约束 |
| 3841c63 | 数据库迁移、后端一致性及安全修复、Linux验证镜像 |
| 872da7c | 前端阅读闭环、书源/书架/设置管理、测试及生产镜像 |
| d0c183b | Go版本统一、race/frontend/security工作流 |

提交前检查272个当前候选/已跟踪文件及401个历史文件对象，未发现所检查的私钥、GitHub token、AWS访问键格式或references/.toolchain路径。该格式检查不等于覆盖所有形式的凭据审计。真实测试凭据和结果均留在忽略目录。

## 可移植性与镜像构建

Windows开启core.autocrlf，导致原architecture.md、openapi.yaml及research/summary.md为CRLF，Linux检出后冻结摘要不同。本轮仅将这三份冻结文件规范为LF并登记新摘要，.gitattributes约束文档换行；无接口/DDL内容变更。数据库版本1、已发布迁移基线及Frozen v1.5修订2语义不变。

从d0c183b用git archive导出实际已提交内容，核对全部10份冻结摘要一致，API TypeScript重新生成与已提交版本一致。arm64生产镜像从此快照构建，revision标签指向完整提交`d0c183b79020539f6d90f8c606230269e490b956`。

构建阶段运行于BUILDPLATFORM；Go通过TARGETOS/TARGETARCH交叉编译，前端在构建机产生架构无关静态资源。运行阶段使用目标平台镜像，不将amd64二进制装入arm64产物。

## arm64实际产物

| 产物 | 镜像ID | 全严重度扫描 |
|---|---|---|
| yreader-pre-release:backend-arm64 | sha256:334e2b28072533d9ffbc8b4d3c32c9a6073c7cad298a6c9d47dbd275aa58a882 | 0发现 |
| yreader-pre-release:frontend-arm64 | sha256:5e1ba1dbdcbdcbc0ac6a247f5cd7109810727300dad1d4a5ea7ac1b41a7c36e2 | 0发现 |

使用固定Trivy0.76.2及其固定镜像摘要，显式linux/arm64，扫描OS/Go包全部严重度，不忽略未修复项。原始JSON在`.toolchain/pre-release-reports/*-arm64.json`，构建日志在`.toolchain/pre-release-*-arm64-build.log`。

Docker Desktop在amd64宿主上通过模拟执行实际arm64产物，不冒充实体arm64设备验收。后端确认aarch64、UID10001、Alpine3.24.4，使用独立smoke数据库实际通过生产配置启动、readyz、登录、书架、TXT导入、详情及删除。前端实际运行arm64 Nginx并通过配置检查。

## Linux race

backend/Dockerfile.verify使用Go1.26.11、build-base和CGO_ENABLED=1。将精确已提交backend快照只读挂载到/src，对无宿主端口的专用PostgreSQL16/Redis7运行完整串行集成，保留用例内部并发。命令为`go test -race -tags integration -p 1 -parallel 1 -json ./... -count=1`。

首轮实际结果为195个顶层通过、1个辅助worker跳过、1个迁移测试失败，未报告数据竞争。唯一失败是验证容器没有挂载docs，读取../../../docs/schema.sql失败；修复挂载后重跑，不将此轮记作通过。原始事件与标准错误分别保存在`.toolchain/pre-release-linux-race.jsonl`及对应stderr日志；不以镜像构建成功冒充race通过。

## 远程CI与收尾

已确认连接账号为dgulan，用户后续指定公开空仓库https://github.com/dgulan/YLReader，拥有推送权限，并已配置origin。远程实际结果待执行；不以本地验证冒充GitHub Actions通过。

远程目标确认后，优先以独立分支/可审阅PR运行既有三份工作流，持续跟踪到终态；失败按具体日志修复，不跳过安全扫描或放宽时间断言。分支保护、发布和生产迁移不自动修改。
