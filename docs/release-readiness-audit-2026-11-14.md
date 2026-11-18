# YLReader 发布就绪审计（2026-11-14）

审计对象为当前工作树（HEAD `fdee4bd`，分支 `codex/pre-release-20261113`）。方法：静态审阅 backend/frontend/docs/CI/部署文件，运行本地类型检查、单元/集成测试与构建，复核第 1 阶段镜像证据和第 2 阶段性能基线。未读取 `references\`，未使用真实书源或生产数据，未部署、提交、推送或合并。

## 结论

**当前不满足正式发布条件；也不满足“小范围试用”的完整门禁。** 代码主体、测试基线和沙箱隔离已具备可用基础，但第 3 阶段（真实阅读）、第 4 阶段（备份恢复生产闭环）尚未执行，第 1 阶段还剩 ARM 原生门禁，且工作树存在单副本风险与部署/安全缺口。

| 阶段 | 状态 | 已有证据 | 主要缺口 |
|---|---|---|---|
| 1 worker 镜像验收 | 基本完成，1 项门禁未闭 | amd64/arm64 最终镜像构建成功；Trivy 双架构 0 高危；amd64 运行时验证通过 | arm64 仅在 x86 模拟下验证，statfs 根只读标志不符，需真实 arm64 主机复核 |
| 2 资源与性能验收 | 合成验收完成 | 两轮全量复跑：normal 10800 次中 10637 成功、163 次受控超时（均在单 worker 并发 16），四 worker 并发 1/4/16 全部 100/100；父进程在途字节预算（默认 64MiB）生效，紧预算稳定 1 成功 + 3×504，12MiB 并发最多 2 个在途；4/8/16MiB 输入接受、31MiB OOM 后恢复、32MiB 快速拒绝；已给出资源配置与拒绝边界，并决定 worker 暂不设为默认 | 真实书源、中文长正文与长时运行归第 3 阶段；ARM 性能未测 |
| 3 真实阅读验收 | 未开始 | 阅读器、进度同步、离线缓存有单元/组件测试 | 无合法书源、TXT/EPUB、移动真机、长时间阅读、断网恢复实测 |
| 4 备份恢复生产闭环 | 原型/设计完成，闭环未验 | 备份格式、加密信封、本地/对象存储原型与多项验证文档 | 恢复预览、冲突处理、失败回滚、隔离环境恢复演练与操作说明未验证 |
| 5 发布范围收口 | 未决 | 旧版导入 CLI 已存在；S3 明确未实现 | 首发是否含 S3/导入 CLI/大书异步导入未签字；当前导入为同步 90s 上限 |
| 6 最终审计与发布准备 | 未开始 | — | 无 RC、无发布说明、无升级/恢复文档终稿；196 项工作树变更未提交 |

## P1：发布阻断项

1. **全部阶段 1/2 工作未提交，且只有本机一份。** `git status` 共 196 项（约 65 个已跟踪修改 + 131 个未跟踪），含 worker、备份、准入预算、文档与 CI 改动；HEAD 仍为 `fdee4bd`。磁盘或设备故障会丢失全部未提交成果，且无法评审、回滚或复现。发布前必须按逻辑分批提交、开 PR、打 RC 标签。
2. **仓库缺少 LICENSE 与根 README。** 根目录仅有 AGENTS.md、Makefile、compose 等；远端 `dgulan/YLReader` 仓库没有许可证文件，属法律与分发阻断；README 缺失使部署/试用入口不存在。
3. **生产编排不具备发布级隔离与资源边界。** `docker-compose.yml` 中 `YREADER_SANDBOX_SOCKETS` 默认为空，即生产默认仍是进程内 goja（未启用 worker，而不是回退）；编排没有 worker 服务、没有 `mem_limit`/`cpus`、app 未设置 `read_only`/`cap_drop`/`no-new-privileges`；PostgreSQL 口令固定为 `yreader`；frontend 使用官方 nginx 镜像、master 以 root 运行。ADR-021 要求 worker 显式启用且无回退，但编排没有给出可用的 worker 配方与资源/容量依据。
4. **Web 安全基线缺失。** `frontend/nginx.conf` 与 API 响应均未设置 CSP、HSTS、`X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`；未发现登录失败限流或账户锁定（仅生成代码中存在 `ErrorCodeRateLimited`）；访问令牌存于 `sessionStorage`，任何 XSS 都可直接读取。公网或反代暴露前必须补齐。
5. **ARM 原生门禁未闭。** 第 1 阶段只在 x86 模拟下发现 statfs 根只读标志不符，未在真实 arm64 主机复核；不能宣称 arm64 镜像可发布。
6. **第 3、4 阶段未执行。** 真实书源/TXT/EPUB/移动真机/断网恢复没有验收记录；备份恢复只有原型与设计，没有从备份恢复到隔离环境并校验完整性的演练。按用户定义，这两项是“正式发布”必需。
7. **生产升级路径缺失。** `docs/schema.sql` 仅通过 `docker-entrypoint-initdb.d` 在空数据卷首次初始化；`make migrate` 只面向测试/隔离库，没有面向既有生产库的版本化迁移与回滚说明。

## P2：应修但不单独阻断发布

- `VerifyPassword` 从数据库 PHC 串解析 Argon2 参数且未设上限；数据库被篡改时可造成 CPU/内存耗尽（低概率，建议加上限）。
- 本地导入为同步请求，`LOCAL_IMPORT_TIMEOUT=90s`、上传上限 100MiB，无断点续传/异步任务；大书导入会占用请求线程并在超时后要求人工核对书架。
- worker 的 128MiB 限制不覆盖父进程输入与 IPC 编码：边界阶段父容器高水位 216–232MiB，31MiB 逻辑输入会让 128MiB worker OOM 并重启（存活 worker 峰值 116–119MiB）。需要给出父/worker 的配套容量建议，并在编排中落实。
- `docs/deployment.md` 自述“开发中部署说明”，无 worker 部署配方、无反代/HTTPS/备份定时任务示例。
- S3 驱动不存在（`internal/pkg/storage` 仅 localfs/backup）；若首发承诺对象存储，需另立阶段。
- 沙箱的输入上限、在途预算与并发槽没有环境变量入口：`main.go` 只读取 `YREADER_SANDBOX_SOCKETS`，`sandbox.Config{}` 的 `InputLimit=32MiB`、`InflightBytes=64MiB`、`MaxConcurrent=4` 均为编译默认，运维无法在不改代码的情况下按机型调整；第 2 阶段的容量结论目前只能以默认值为准。
- `TestSandbox_InfiniteLoopTimeout` 在 CPU 争用下曾报 4.075s（期望 3s±300ms），隔离重跑稳定通过；属时序测试脆弱性，需放宽或改为事件驱动断言。
- 契约文档（architecture/openapi/contract-freeze/deployment）改动未提交；ADR-020/021 已写入 `contract-freeze-v1.5.md`，但尚未经提交后的冻结评审；openapi 仅有描述性改动。
- 前端构建中 dayjs 相关 chunk 363.29kB（gzip 48.47kB），nginx 未启用 gzip/静态缓存；非阻断，但首屏可再优化。

## 已验证项（本轮）

- backend：`go vet ./...` 通过；`go test -count=1 ./...` 全绿（修复了 `TestImportDefaultBudgetAndNonTimeoutError` 的 TMP 环境依赖，测试内 `t.Setenv("TMP", t.TempDir())`）。
- frontend：`npm run type-check` 通过；`npm test` 15 个文件 77 项全过；`npm run build` 成功（1686 modules）。
- 沙箱：新增 `InflightBytes` 预算（默认 2×InputLimit，生产 64MiB）与 250ms 准入拒绝；两轮全量脚本通过，四 worker 六种负载 × 并发 1/4/16 全部成功；紧预算 4MiB 用例稳定 1 成功/3 次 504；12MiB 并发下父容器峰值 141–153MiB、worker 峰值 80–84MiB；normal 阶段父容器峰值 25.4–35.1MiB。
- 容器：amd64 最终镜像运行验证与 Trivy 扫描结果沿用第 1 阶段记录。
- 本地 `go test -race` 因缺少 gcc 无法执行（DEF-CODE-10），race 证据仅来自 CI/Linux 容器。

## 建议顺序

1. 先提交收口（P1-1），把第 1/2 阶段成果变成可评审的 PR；随后补 LICENSE/README。
2. 修 P1-3/P1-4：worker 编排配方、资源上限、密钥与口令、安全响应头、登录限流。
3. ARM 真机复核（P1-5）。
4. 第 2 阶段已收口（合成基线、容量与拒绝边界、worker 暂不设为默认的决策均已落盘），下一步进入第 3 阶段真实阅读验收。
5. 第 4 阶段做隔离环境恢复演练；第 5 阶段签字首发范围；最后出 RC 与升级/恢复文档。

## 限制

本审计未覆盖真实书源、真实中文长正文、移动设备、ARM 主机与多实例部署；性能数据来自单机 Docker Desktop 合成负载，不能当作生产 SLA。备份恢复结论仅基于文档与原型，未执行端到端恢复。
