# 交接数据库身份探测验收

日期：2026-11-16。分支 codex/pre-release-20261113；HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。沿用已有未提交工作树；未部署、提交、推送或访问业务数据库。

## 已实现

按 [先行内部协议](backup-database-identity-design-2026-11-16.md) 新增 database_identity.go、对应单测/隔离集成测试及 scripts/verify-backup-database-identity.py。入口只接受现有 ParseTarget 允许的显式回环隔离目标，要求独立登记的 source/target cluster system_identifier + database OID + role OID。源和目标指向同一实际数据库时，即使角色不同也拒绝。

真实探测使用五秒超时、RepeatableRead + ReadOnly 事务，固定查询核对实际身份、DSN 用户/数据库、角色安全属性/所有权/成员关系、其他会话及迁移账本。账本先检查为角色拥有的非 RLS 普通表，再验证唯一基线记录；不执行迁移、修复、GRANT 或 SET ROLE。失败只返回固定错误和空摘要，不泄漏连接诊断。全部通过仅返回非敏感身份元数据的规范 v1 SHA-256，可用于交接模型的 Database 绑定输入。

## 实际隔离验证

最终目录：`.toolchain/backup-database-identity-f328c1e494a5/`。result.json passed=true，13 个通过事件（1 个顶层、12 个子测试），0 失败/跳过。

- PostgreSQL 16.15，复用本地缓存镜像 `sha256:f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657`，未拉取新镜像。
- Linux amd64 测试二进制 SHA-256：`f87578b716f0cedd72f84824828d5f735ecb023171223d063520b43503cadcb5`。
- 数据库为本轮新建、network=none、无宿主端口、数据 tmpfs。测试容器共享它的隔离网络命名空间，UID 10001、只读根、cap-drop=ALL、no-new-privileges；仅挂载只读测试二进制，不读取业务配置。
- 验证成功只读探测；拒绝函数权限不足、database/cluster/role 漂移、同库其他角色别名、其他会话、坏账本、账本被视图替代、CREATEDB 角色、角色成员关系及取消。比较真实测试数据（中文/emoji 哨兵行）、账本和函数 ACL；探测前后未变化。不将局部快照说成所有业务表逐字节证明。
- evidence.json 明确 scope=isolated-database-identity-fixture，readonly_fixture_unchanged=true，deployment_authorized/ready_for_cutover=false。两个本轮容器按标签核对后清理，cleanup 均为 true。

首轮 `.toolchain/backup-database-identity-4b6db5776fda/` 未通过：权限反例只撤销 reader 的直接 EXECUTE，未排除 PUBLIC 继承的执行权限。修正自有夹具为先撤销 PUBLIC、单独授予 reader；反例再次撤销 reader 后，先用 has_function_privilege 确认实际无权，再验证探测拒绝。生产探测逻辑未放宽。失败记录保留，首轮两个容器也均清理成功。

## 回归和复现

- Windows Go 1.26.11：`go test -mod=readonly -json -count=1 ./internal/backup`，52 个顶层、248 个含子测试通过事件，0 失败/跳过。日志 `.toolchain/backup-database-identity-windows-20261116.jsonl`；本轮新增 3 个顶层身份单测。
- `go vet -tags=integration ./internal/backup`、gofmt、Python py_compile、git diff --check（core.safecrlf=false）通过。
- `python scripts/verify-backup-contract.py`：65 项检查通过、16 个冻结摘要一致。未修改公开 API、SQL DDL、基线、已冻结备份格式或任何操作状态。

backend 工作目录下，使用本地 Go 1.26.11 和仓库模块缓存，设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0 后构建：

```text
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-identity-linux.test ./internal/backup
```

回到项目根运行 `python scripts/verify-backup-database-identity.py`。脚本不接受 DSN、不拉取镜像，只创建全新隔离夹具；编译与运行需分别完成。内部 fixture 才有建库、建角色、迁移及故障注入权限，探测器没有这些操作。

## 未覆盖与下一步

本轮没有运行 Linux race、完整后端集成回归、真实归档恢复全链、前端或 iPhone 真机验收。身份 pin 的可信预登记、物理克隆/OID 重用、管理员和新非协作连接仍需外部隔离与恢复守卫；迁移账本匹配也不等于完整 DDL/约束指纹验证。

运行配置与 Redis 身份、对象/header 的实时交接核验、报告/state 持久 CAS 和只读 CLI 仍未闭环；本探测器尚未自动生成通过报告或放行恢复。下一步接入运行配置与 Redis 核验，随后再在真实恢复夹具中编排完整报告。第 3 阶段的 iPhone Safari 真机状态保持未执行。
