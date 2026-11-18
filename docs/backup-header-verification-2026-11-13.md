# 真实 header 解密恢复门禁（2026-11-13）

依据 [实现前设计](backup-header-gate-design-2026-11-13.md)，本轮交付私有 keyring 加载、全量 header 验证及 RunDatabaseWithHeaders 事务路径。没有发布对象、开放完整恢复入口、启动恢复实例、修改部署、提交、推送或合并。

## 实现与安全边界

- 复用项目既有 AES-256-GCM v1 信封及 user_id|source_id AAD，不引入另一套加密格式；crypto.Keyring 仅新增 nil-safe 的 HasKID 覆盖查询，不暴露密钥字节。
- 专用 keyring 文件显式加载，通过安全 secret 文件驱动检查私有权限及链接。严格 UTF-8 JSON，拒绝重复/未知/大小写变体字段、重复 kid、非法 base64、错误密钥长度、无效 current_kid；文件最多 64 KiB、64 个 key。没有环境变量或全局默认 keyring 回退，也不修改冻结 Config 结构。
- 先确认 manifest 所需全部 key 存在，再允许进入数据库执行。缺 key 时记录仍为 not_started，没有建立业务结构或导入数据；已经暂存的对象保持隔离，不能据此标为已恢复。
- 在同一恢复事务内、数据加载和对象引用检查之后、会话撤销及 COMMIT 之前，逐行验证全部 book_sources.header。空对象允许；非空对象只能含一个 __enc 字符串。拒绝非空历史明文、混合字段、未知版本、未声明 kid、认证失败及非法解密 JSON。
- 按数据库真实 user/source UUID 生成 AAD，不重映射 ID，不解密后重新写入或重加密 header。实际信封 kid 集合必须精确等于 manifest required_header_kids，不能只检查 manifest 名字或声称抽检通过。
- 使用单信封 JSON 128 KiB、解密 JSON 64 KiB 的隔离实验上限。数据库查询超额时不把完整字段传入客户端；SQL 表达式仍需服务端处理 JSONB，这不构成 PostgreSQL RSS/CPU 配额。合法但超限的历史数据可能被拒绝，未作大容量兼容或性能承诺。
- 不将明文、密钥、header 内容、用户/书源/kid 值写入日志或结果。底层错误归一为稳定哨兵；尽力清空解密 byte 缓冲及读取的 keyring 文件缓冲，不承诺 Go 堆副本或介质的安全擦除。
- 事务中的 header 错误使整体数据库事务回滚，并按既有 rolled_back 持久登记及对象清理规则处理；提交/回滚未知仍保留现场。header 错误映射为 validation_failed，不放宽结构、外键或其他门禁。

RunDatabaseWithHeaders 的 HeadersChecked 只在本次真实执行完成全量解密、数据库明确提交且结果/状态登记成功时为 true。读取已有 committed 状态不会重新解密或产生该标志。它是内部瞬时检查结果，不是持久发布凭证或整体恢复完成证明。既有 RunDatabaseRecorded/数据库原语仍供隔离诊断，不能据其局部成功绕过此门禁发布实例。

非空历史明文需要在源侧按既有 header 迁移流程处理，不在恢复时自动转换、猜测 key 或回退明文。

## 验证

- Windows backup/migration/storage/crypto 四目标包：44 个顶层、166 个含子测试通过事件，0 失败/跳过；目标包 vet 通过。日志 `.toolchain/backup-header-windows.jsonl`。文件驱动原生验证仍限 Linux，不冒称 Windows 安全恢复可用。
- Linux 四目标包 race：70 个顶层、204 个含子测试通过事件，0 失败/跳过/竞争报告；日志 `.toolchain/backup-header-linux-race.jsonl`。非 root、无网络、源码/docs/模块只读。
- 单测覆盖正确解密、错误 key、错误用户/书源 AAD、明文、混合信封、格式及版本错误、非法/超额解密 JSON、未声明/未使用 kid 和缺 key；错误文本不含测试私密 header 标记。
- Linux 私有 keyring 文件测试在环境及进程默认 keyring 故意配置错误密钥时仍使用显式文件正确解密；将文件改成 0644 后被拒绝。没有读取或改写真实密钥文件。
- 最终真实集成记录 `.toolchain/backup-restore-db-a821ae5b4295/tests.txt` 与 result.json：4 个顶层、21 个子测试，共 25 个通过事件，0 失败/跳过。两个本轮自有容器清理均为 true。
- 在独立 PostgreSQL 16.15 tmpfs 集群的合成源副本中，用既有 Go crypto.Seal 为两个用户/书源生成 primary、legacy 两代真实 GCM 信封，重新 pg_dump，封装进 age/tar，使用私有 keyring 文件贯通真实计划/锁/对象暂存/事务/header 门禁/结果登记。
- 正确 keyring 提交成功并保持 validating；错误 key、错误 AAD、实际 kid 未在 manifest 中和非空历史明文分别触发真实事务回滚，数据库重新 pristine，已确认失败的对象 generation 清空。缺 legacy key 在数据库执行前拒绝，记录 not_started、数据库 pristine、暂存保留。
- 正确提交后的到期重复请求返回已有状态，但 HeadersChecked=false、Executed=false，不把重放当成新验证。原有 18 个数据库/对象/持久结果事件继续通过，包括提交后控制区确认丢失和不重放。
- 测试集群无外网、无宿主端口，数据库数据仅 tmpfs；测试 UID 10001，源码/归档挂载只读。生成 dump 和 keyring 的测试目录显式 0700、文件 0600，源 DSN 使用同样的显式回环/隔离名称解析。fixture 超级用户仅用于专属合成源，目标恢复仍是非特权数据库所有者。
- 65 项契约模型检查及 16 份冻结摘要匹配，OpenAPI、DDL、历史迁移和公开备份结构保持。

本轮使用的全是真实加密的合成凭据，不代表生产密钥、真实书源网络兼容性或应用登录已验收。没有进行物理断电或 COMMIT 网络断线演练。

## 对象发布的下一步

现有 generation 仍是 opaque-id 平面暂存，不能直接作为业务对象根使用。已在设计中确定：用已验证目录句柄逐层创建原 key 布局、拒绝覆盖/链接/前缀冲突，逐对象回读与同步，独立登记发布 generation 和完成记录，并处理进程中断窗口；不能复用可覆盖的普通 LocalFS.Put 充当安全发布。

下一步实现该发布能力，再在隔离实例验证真实 Repository header 读取及本地章节访问。即使 header 门禁和数据库都成功，仍不能生成 ready_for_cutover/completed 或修改部署配置。
