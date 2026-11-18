# 数据库结果登记与只读中断核对（2026-11-13）

本轮把真实数据库原语接入 restoreGuard/attempt，增加 RunDatabaseRecorded、只读数据库观察和 generation 单一目录校验。公开冻结接口、DDL 和历史迁移未改；没有提供 CLI/HTTP apply、发布对象、启动恢复实例、提交、推送、合并或部署。

## 执行与持久顺序

1. 从保留的控制区读取 operation/attempt，精确匹配 owner、计划、包、目标指纹及 generation。已是 unknown/committed/rolled_back 的请求只返回已有结果，不再执行；终局只读查询可以发生在计划到期之后。unknown 返回需要核对的稳定错误，不当作可重试失败。
2. 对尚未执行的请求，重新检查计划有效期、确认值、目录租约、五个新打开的配置路径与保留句柄、包暂存归属及真实空库。此时目标允许非空，但安全驱动必须确认仅有登记的 generation，且实际目录身份与其保留句柄匹配；额外文件、链接或其他目录均拒绝。检查 owner 文件及包绑定，不接受仅名称相同的暂存位置。
3. 读取并校验同一份目标 DSN，实际数据库调用使用这次读取的连接串，不在执行前换读另一个配置。取消标志与操作身份在意图发布前后再次检查。
4. 先 CAS 持久登记 unknown，再允许数据库原语执行。登记返回任何错误时不执行数据库；准备句柄同时锁定为不可盲重试，即使随后读取仍是 not_started，也不能再次发起写入。
5. 使用现有可信基线、固定 COPY、序列及引用门禁执行真实事务，期间保留目录租约、包和 generation 读取句柄。明确提交后用独立 5 秒上下文登记 committed；确认失败/回滚后登记 rolled_back，不被原调用上下文取消阻止。
6. 结果或随后 operation 状态发布失败时，返回 ErrOutcomeRecord 并保留对象；不能把普通返回错误解释为允许清理。已明确提交的数据库结果可在本次返回中提供，但不会因此发布 ready_for_cutover。COMMIT 或回滚不能确认时，attempt 继续 unknown，不清理、不重放。
7. 只有 rolled_back 已持久登记且失败/取消状态也已确认发布后，才清理本轮 generation。清理自身失败返回稳定残留引用，不能清理源或其他目标。提交成功继续停留 validating，等待后续密钥、发布及应用验收门禁。

同一准备句柄的数据库重入由互斥锁和本地执行闩锁阻止；跨进程仍依赖目录租约、状态读取及既有新空目标约束。新进程没有自动接管、续跑或修复入口，不根据陈旧心跳取得执行权。

## 只读观察

observeRecordedDatabase 使用显式回环 URI、隔离库名白名单、非特权所有者和 PostgreSQL 16 检查，开启 RepeatableRead/ReadOnly 事务，仅返回：

- pristine：在观察快照中满足新空库检查。
- populated：账本兼容且 14 张表计数可读，另返回是否与期望计数一致。
- busy：观察到其他数据库连接，避免把运行中状态猜成空库或已完成。
- incompatible：非空且账本不兼容或权威表不可读；连接/权限等无法确认时返回稳定错误，不猜为空库。

这是时间点证据，不证明完整数据、结构或特定 operation 的 COMMIT；不会改写 attempt/operation，不会将 unknown 自动变成 committed/rolled_back，也不授权清理、接管或切换。计数一致不能替代引用/密钥/应用验收。

## 验证

- Windows backup/migration/storage：37 个顶层、142 个含子测试通过事件，0 失败/跳过；目标包 vet 通过。日志 `.toolchain/backup-outcome-windows-final.jsonl`。本轮未将非 Linux 能力拒绝当作 Windows 恢复可用。
- Linux 三包 race：62 个顶层、179 个含子测试通过事件，0 失败/跳过/竞争报告；日志 `.toolchain/backup-outcome-linux-race-final.jsonl`。非 root、无网络、源码/docs/模块只读。
- 原生安全文件驱动上的 12 个故障/执行模型场景：正常提交、确认失败、提交未知、回滚未知、意图发布前失败/确认丢失、结果发布前失败/确认丢失、operation 发布失败、回滚结果/状态发布失败，以及执行中调用上下文取消。验证 unknown 在执行前已持久保存、活动句柄阻止对象清理、未知结果保留、确认失败清理和重复调用不执行。
- 上述模型注入数据库回调，不代表真实 COMMIT 网络故障。另有额外目标文件拒绝测试，验证拒绝发生在意图登记之前且不删除 generation。
- 真实隔离 PostgreSQL 集成：3 个顶层、15 个子测试，共 18 个通过事件，0 失败/跳过；记录 `.toolchain/backup-restore-db-96f8b25de490/tests.txt` 和 result.json。两个自有容器清理均为 true，数据库数据仅 tmpfs，network=none、无宿主端口，测试 UID 10001、挂载只读，没有访问业务实例。
- 新增真实流程贯通：真实 pg_dump 包认证→真实 PreparePlan/空库检查→目录租约→对象暂存与持久登记→真实事务→committed/rolled_back 记录。缺对象导致真实事务回滚并清理；有效样本保持两个用户、char_offset=17/version=7 的进度和更新后的 token_version=4。
- 在真实事务成功 COMMIT 后注入原生控制区 CAS 确认丢失，及后续 operation 发布失败，核对数据库数据和对象均保留。再次请求即使计划过期且存在竞争连接，也返回既有 committed 记录，不重新检查/加载数据库。还验证 unknown 意图发布前失败时数据库 pristine，后续请求被本地闩锁阻止。
- 故障注入模拟 CAS 返回错误/确认丢失，底层使用实际安全驱动；不冒充物理断电、文件系统损坏或真实 COMMIT 网络断开。真正 COMMIT/回滚结果未知的分支目前通过模型验证，仍需专项故障演练。
- 只读观察实际验证 pristine、populated/计数一致、busy 和不兼容 marker 结构；观察前后 attempt CAS token 相同。已有 marker 保留，观察不修复或覆盖数据。
- 既有数据库/对象 12 个集成事件继续通过，未放宽先前外键、整数溢出、跨用户引用等断言。65 项契约模型检查及 16 份冻结摘要匹配。

## 下一步

接入真实 header keyring 解密检查和安全的业务对象布局发布，最后验证恢复实例的登录与本地章节读取，再形成交接报告。当前 generation 仍使用 opaque-id 平面暂存布局，不能直接当作现有应用的对象根；header 样本仍是明确不可解密的合成信封。

后续失败若数据库已 committed，必须保持隔离并报告残留，不能把数据库局部成功撤销为“已整体回滚”。尚未完成整套备份恢复发布门禁，服务入口继续关闭。
