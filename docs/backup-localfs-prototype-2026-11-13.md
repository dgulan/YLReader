# LocalFS 权限、别名与计划状态验证（2026-11-13）

范围：设计原型，无业务数据库连接，无部署；所有文件均为本轮唯一临时目录内的合成内容。验证脚本只验证文件系统行为和纯状态模型，不提供可用的备份恢复工具。

## 最终结果

- Windows PowerShell 7.6.9：12 项检查通过，唯一临时目录 cleanup=true，完整报告在 .toolchain/backup-localfs-windows.json 及带 run_id 的独立 JSON。
- Linux：使用本机已有 PostgreSQL 镜像仅执行 sh，不启动数据库；network=none、只读根、UID 10001、tmpfs /prototype、无宿主挂载或端口、--rm。9 项检查通过，日志 .toolchain/backup-localfs-linux-final.txt。
- manifest/config/plan/operation：共 65 项契约与纯状态模型检查通过，日志 .toolchain/backup-contract-checks.json。

## Windows 发现与修正

1. .NET Framework 与 PowerShell Core 的创建目录 ACL API 不同，初次调用无匹配重载，没有创建明文目录。初次原型的 finally 还错误地把“未创建目录”当成清理失败，已修正并保留初始报告。
2. 在当前管理员账户的默认行为下，继承私有 DACL 的文件 owner 为 Administrators，而不是当前用户 SID。改为创建空文件时显式指定 owner/protected DACL、CreateNew 和不共享，核验后写入；没有放宽检查。
3. 用 Get-Acl 再 Set-Acl 构造负向样本时触发 SeSecurityPrivilege 要求；改为在创建测试目录时直接附加完整 ACL，没有请求或提升该特权。
4. 沙箱不允许创建目录联接，在审查允许后仅于唯一临时目录中执行。文件祖先扫描初版使用 FileInfo.Parent，未检查到父目录；改用 FileInfo.Directory，重跑后拒绝联接目录和联接下的文件。失败报告未删除。
5. 当前脚本证明了显式 owner/DACL、拒绝其他 Users 读权限、排他创建、真实联接别名拒绝、联接清理不影响目标、硬链接计数及移除后原数据完整。

Windows PowerShell 5.1 尝试执行时被本机 AuthorizationManager 策略阻止，没有绕过该策略，也不把这一运行时计为通过。当前 Core 运行时的结果不能替代 5.1 实测。

Windows 创建时附带安全描述符的依据见 [Microsoft FileSystemAclExtensions.Create](https://learn.microsoft.com/en-us/dotnet/api/system.io.filesystemaclextensions.create)。不依赖先创建可公开读取文件、再收紧权限的窗口。

## Linux 结果与边界

非 root 环境验证目录 0700、文件 0600/UID、宽松 mode 可检测、符号链接及文件祖先链接识别、硬链接 nlink=2、排他发布不覆盖原文件、移除链接不影响目标。

第一次通过 PowerShell 文本管道传递 shell 脚本时被额外 CRLF 破坏，退出 127，未计为通过。改为 Python subprocess 传递原始 LF 字节后完整通过；未修改权限/别名断言。该 Linux 原型没有测试额外 POSIX ACL，也不证明 NAS 的权限、目录 fsync、掉电持久性或实际 CAS 实现。

## 计划与状态模型

新增 restore-plan.json、restore-operation.json 及 schema 分支。计划摘要递归按键排序，ASCII/UTF-8、无空白、整数十进制，排除 plan_digest 自身；存储物理位置只存摘要，计划中不含 DSN 或私钥。有效期 900 秒，过期边界拒绝；测试覆盖篡改目标、错误摘要、非隔离库/外部 host、时间边界和错误确认。

状态模型覆盖主线四次转换、禁止跨阶段、stale generation、终态不可写、取消请求及已取消终态、取消后不再推进、超时不等于确认进程死亡。generation/摘要等绑定校验是模型，不是线程或进程间真实 CAS 测试。

静态路径遍历不能消除 TOCTOU，chmod/mode 也不能替代 Windows ACL 或额外 POSIX ACL。契约要求正式实现绑定已验证句柄、确认物理身份、拒绝无法验证的后端，再进行真实权限、并发和崩溃验收。
