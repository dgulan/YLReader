# 原 key 业务对象发布验证（2026-11-14）

依据 [发布内部协议 v1](backup-publication-design-2026-11-14.md)，本轮实现安全 PublicationTree、独立 publication.json 以及 restoreGuard.PublishObjects。对象发布这一步已可在隔离实验中完成，整套恢复仍没有公开 CLI/API，没有 ready_for_cutover/completed、服务启动、部署、提交、推送或合并。

## 发布链

1. 新发布要求匹配的 committed attempt、validating 操作，以及同一 guard 中刚完成真实 header 核验并成功登记结果的内部证明。单纯数据库结果或状态重放不能取得该证明；取消或额外目标内容会被拒绝。
2. 持有控制/目标租约、包和原平面暂存句柄，重新核对计划、配置路径身份、目标唯一 generation 和所有权。再次只读检查真实数据库所有权/权限/版本、账本和 14 表计数、header 解密、对象引用，以及会话已撤销、无排队/运行任务。
3. 全部原 key 先校验，再写发布记录。当前 profile 限定 4096 字节、64 层、每层 255 字节；拒绝非法字符、点路径、重复与文件/目录前缀冲突，不静默改名。前缀检查覆盖 `a, a-b, a/b` 这类非相邻冲突，不只比较排序后的相邻项。
4. 控制区先排他发布 planned 记录，再新建 `.publish-<操作UUID去连字符>` 私有目录。持久登记实际目录身份摘要和 copying 后，才写任何对象字节。源 key 仅传给安全驱动；业务层不拼宿主路径、不复制目录、不使用普通 LocalFS.Put 覆盖写入。
5. 目录 FD 下逐层不可覆盖创建，复制时核对大小/SHA-256，随后回读目标逐文件校验。文件和创建过的目录均通过安全驱动同步，树内不写元数据文件，避免占用原 key。
6. 改名前再次检查保留的位置身份、数据库/密钥及操作取消状态，持久登记 publishing，才使用同卷 renameat2 RENAME_NOREPLACE 改为 `objects-<操作UUID>`；父目录同步失败返回 CommitUnknown，不能覆盖已有最终目录。
7. 改名后再回读全量对象，最后 CAS 发布 verified。目录可见不等于完成；使用者还须核对该记录并完成后续应用/交接门禁。

重复请求只读已有记录，不重建或改名；未 verified 仍返回待核对。第一次登记即使报错且重读不存在记录，同一 guard 的闩锁也禁止盲重试。新进程没有自动接管/续跑入口。

任何错误都保留原平面暂存与 pending/最终树；Close 只关闭树/父目录 FD，不删除数据。接口故意不提供递归清理或覆盖方法。目录创建到身份登记之间崩溃可能留下空目录；不能推测其归属后删除。错误归类进入保留分支，数据库已经 committed 时不能以对象发布失败为由声称整体回滚。

此 root 是新的独立业务对象根，原应用配置及卷未改。新实例必须显式选择已核验根并通过剩余门禁，不会自动切换过去。

## 验证结果

- Windows backup/migration/storage/crypto：46 个顶层、168 个含子测试通过事件，0 失败/跳过，目标包 vet 通过；日志 `.toolchain/backup-publication-windows.jsonl`。Windows 仍只覆盖模型与能力拒绝，没有原生安全发布驱动。
- Linux 四包 race：76 个顶层、212 个含子测试通过事件，0 失败/跳过/竞争报告；日志 `.toolchain/backup-publication-linux-race.jsonl`。非 root、无网络，源码/docs/模块只读。
- 原生驱动验证：嵌套 key 写入和读取、最终名称在 Commit 前不存在、同 key 不可覆盖、已有最终目录/marker 保持原值、提交后禁止新增写入或再提交、父目录链接拒绝、Close 不删除已发布数据。
- 注入改名后的父目录同步错误，返回 CommitUnknown，实际最终文件仍在且禁止再次 Commit；没有将一次读到文件解释成发布完成。
- 子进程真实创建/写入 pending 树后被强制终止：最终名称不存在，pending 内容保留，同 operation 再创建返回 Conflict。它验证存储原语的进程中断，不冒充整个数据库恢复、物理断电或控制区持久性故障的完整演练。
- 模型验证严格 publication JSON、绑定/状态不可倒退、planned→copying→publishing→verified；未核验 header 的数据库成功不能写发布意图，原 key 冲突在树创建前拒绝。
- 最终真实集成：4 个顶层、26 个子测试，共 30 个通过事件，0 失败/跳过；记录 `.toolchain/backup-restore-db-1b43328416e7/tests.txt` 与 result.json。两个本轮自有容器清理 true。
- 正常流程使用真实 pg_dump/age 包、两代 GCM header、真实 PostgreSQL 事务及目录租约完成业务布局发布；以现有 LocalFS Provider 从新根读取原 `local/<book-id>/0.txt` 内容，并用现有 SourceRepository 读取/解密数据库中的 header。测试没有启动 HTTP 服务、改环境文件或连接业务实例。
- 真实事务 committed 后注入控制区各窗口：最初意图登记失败不创建树；copying 登记失败只留下空 pending；publishing 登记失败保留已复制树但无最终名称；verified 写入失败保留最终树和原暂存；确认丢失后重读 verified 只返回状态、不重放。各故障只执行一次对应写入，不盲重试。
- 原有 25 个数据库/对象/header/结果登记事件继续通过；新的 5 个发布窗口与正常发布读取共同覆盖本轮边界。所有目标仍在独立无外网、无宿主端口、数据 tmpfs 的 PostgreSQL 16.15 集群，测试 UID 10001，fixture 和源码挂载只读。
- 65 项冻结模型检查及 16 份冻结摘要匹配；OpenAPI、DDL、历史迁移和公开备份结构未改。

故障注入模拟 CAS 确认丢失及目录同步失败；不代表已做真实 COMMIT 网络断线、断电或 NAS 文件系统兼容性验收。本轮实际本地文件系统验证为 Linux tmpfs；未宣称 S3、Windows、所有 NAS、大容量吞吐或百万对象均已通过。

## 仍需完成

现已具备新业务布局及 Repository/Provider 读取证据。后续 [HTTP 验收记录](backup-http-verification-2026-11-14.md) 已补齐隔离实验的登录/阅读、JWT 和缓存检查；正式交接报告及受控 CLI 仍待完成。需要把真实目标配置和这些证据纳入最终门禁，才可产生 ready_for_cutover。completed、部署与服务切换继续关闭。

发布后的目录和 verified 记录是该子步骤的证据，不是任何人可据以自动启动实例、清理原暂存或重放失败恢复的授权。
