# 恢复排他、计划重验与持久暂存登记（2026-11-13）

依据实现前确定的 [内部尝试记录设计](backup-attempt-design-2026-11-13.md)，本轮交付目录排他能力、持有句柄的 restoreGuard、严格 attempt 记录和 StageRecorded 准备/暂存子步骤。没有开放 API/CLI apply、推进 ready_for_cutover/completed、发布对象、执行部署或提交/推送/合并。

## 目录锁与计划

Linux 安全驱动增加可选 RestoreLocker 能力。锁定已验证目录 inode，不创建锁文件，目标空态检查不受干扰。锁 FD 独立于原驱动句柄、带 CLOEXEC；非阻塞竞争返回 Busy，进程退出由内核释放。只允许明确列出的本地文件系统类型，不提供网络文件系统或非 Linux 降级；实际本轮验证环境是 Linux tmpfs，不冒充所有 NAS/文件系统均已验收。

restoreGuard 在控制区和目标目录持有两把锁，并保留五个原配置目录句柄及已认证包读取句柄。获取锁后及开始暂存前重新检查：

- plan_digest、确认目标 UUID、有效期、包/manifest/迁移绑定及预算；包必须确实以同一份配置预算完成 VerifyPackage，不能仅构造 PackageBinding 或换用不同验证限额。
- 显式目标 DSN 的 host/port/database，以及 TargetInspector 的空库检查。
- 三个冻结位置指纹和五个配置目录的实际身份；重新按配置打开路径，与持有的旧 FD 逐个比较，拒绝配置路径被替换。
- 包的真实私有暂存目录确实位于计划配置的 temporary 目录下；保留句柄防止使用期间清理。

这些是开始暂存前的检查，目标暂存后已非空，不能直接拿同一空态检查作为后续数据库/发布授权。锁是合作式排他，不能防止管理员或同权限操作用户主动修改；独立隔离环境及外层服务门禁仍必需。

## 持久登记

内部 attempt.json 与冻结 operation 状态分开保存，不增加 operation 字段。初始 generation=null/database_outcome=not_started；generation 引用只能登记一次，数据库状态只允许 not_started→unknown→committed/rolled_back。owner、包、计划、目标指纹不可更换；终局结果不能倒退。CreateOnly/CAS 与现有安全控制驱动共用，严格拒绝缺失、重复、未知、大小写变体字段、非法引用、越界内容和不合法转移。

StageRecorded 先持久创建 planned 操作及 attempt，再进入 staging。创建本轮空 generation 后，必须先把其引用持久登记到控制区，才写 owner 记录和任何对象字节；登记失败不能绕过。全部对象通过复制及回读校验后，操作进入 validating，数据库结果仍是 not_started。

同一准备句柄上的重复请求返回既有 operation，不重复暂存、不更换 owner，即使原计划已经到期也只返回状态，不赋予新的执行权限。包绑定或计划不同则拒绝。新进程必须先读取已有 Control/attempt 记录，不能把重新获取锁、陈旧心跳或数据库看似空当作允许续跑/接管的依据。本轮没有新进程恢复入口。

若登记或对象发布耐久性未知，保留 generation 和稳定诊断引用，不自动删除。保留时仍关闭暂存区及父目录 FD，避免描述符泄漏；旧句柄不能再删除这些数据。若进程恰在目录创建与登记之间退出，可能留下未登记空目录，仍须人工核对，不推测归属。

数据库结果转移目前是持久存储层能力，尚未把真实数据库执行接到这套新记录上。因此不能把 validating/not_started 当作数据库恢复完成，更不能据此启动服务。现有数据库/对象内部原语仍只供隔离测试，整体 apply 继续关闭。

## 验证结果

- Windows 三目标包：37 个顶层、141 个含子测试通过事件，0 失败/跳过；日志 `.toolchain/backup-guard-windows-final.jsonl`。目标包 vet 通过。非 Linux 只覆盖模型与能力拒绝，未声称 Windows 安全恢复可用。
- Linux backup/migration/storage race：60 个顶层、164 个含子测试通过事件，0 失败/跳过/竞争报告；日志 `.toolchain/backup-guard-linux-race-final.jsonl`。非 root、无网络、源码/docs/模块只读。
- 真实本地文件系统验证两个独立句柄的互斥、关闭原驱动不解除独立 lease、lease 关闭后可再次获取、锁不产生目标文件、取消拒绝。
- 子进程实际取得目录锁后被父测试强制终止；父进程等待其退出后可立即重新取得锁，目录仍为空。这是目录锁进程死亡测试，不冒充完整恢复在 COMMIT/对象发布窗口硬杀后的恢复演练。
- restoreGuard 测试验证第二执行者 Busy、验证预算漂移/过期拒绝、目标目录重命名并替换后拒绝、原暂存归属核对、真实安全驱动上的持久 generation 登记、planned→staging→validating，以及到期重复请求只读取已有状态且 owner/generation 不变。该组注入只读 TargetInspector，不连接数据库；真实 PostgreSQL inspector 行为此前独立验证，不能据该组宣称整个新控制流程已在数据库端贯通。
- 登记回调执行时，实际 generation 目录仍为空；登记拒绝后没有残留。模拟登记结果未知时目录保留且没有 payload；Retain 原生测试确认子/父目录 FD 都关闭、保留文件字节仍在、旧句柄清理被拒绝。
- attempt 测试覆盖并发 CAS 只能登记一个 generation、旧 token 冲突、终局结果不可倒退、不可换 owner/plan/reference、严格 JSON 和未知结果保留规则。没有将数据库 COMMIT 断线模拟为已完成的真实故障注入。
- 原数据库/对象隔离集成回归：2 个顶层、10 个子测试，共 12 个通过事件，0 失败/跳过；日志 `.toolchain/backup-restore-db-7b34ad72680b/tests.txt` 与 result.json。两个自有容器清理 true。该组复核既有原语兼容性，未调用 StageRecorded 或宣称其执行了数据库事务。
- 65 项冻结模型检查、16 份冻结摘要匹配。OpenAPI、DDL、历史迁移和公开冻结备份结构保持不变。

收尾审查修复两个问题：结果未知时只保留目录却未关闭其目录 FD；encoding/json 默认接受字段名大小写变体。前者新增 Retain 能力与资源测试，后者对 attempt 及既有 Control 加入精确字段名白名单，与原冻结严格字段规则一致。

## 下一步

把数据库步骤接入 attempt 的 unknown/committed/rolled_back 持久登记，在有对象的目标上重新验证保留的位置身份、计划及数据库空态，处理“数据库已提交但状态发布失败”的保留与只读核对。随后完成真实 header 解密、对象业务布局发布及应用读取验收。所有未知状态继续保持隔离，不能自动清理、重放或切换服务。
