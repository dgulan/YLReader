# 恢复控制记录只读命令

本页描述 `status`：它不创建备份、不生成恢复计划、不连接数据库/Redis、不执行恢复/重试/清理或服务切换。新增 `inspect` 与候选 `plan` 见 [只读认证及计划预览](backup-preview-cli-2026-11-16.md)；尚无 apply 命令。

动作响应链使用独立的 [behavior-status命令](backup-behavior-status-cli-2026-11-16.md)，不改变本页status数据结构及退出语义。两个命令都不能单凭文件证明数据库会话已撤销。

## 平台与控制区

已验收 Linux amd64，使用当前非 root 用户拥有的私有控制目录及安全文件驱动。控制目录需符合现有 owner/权限/链接规则（目录0700、文件0600）；工具不自动创建或修复。Windows 原生安全驱动尚未实现，命令明确返回3，不使用普通文件读取降级。ARM/NAS 文件系统未由本轮验证。

`--control-root` 指向含 `operations/<operation UUID>/` 的原始控制区。单独导出的 report.json、日志目录或项目根不是控制区；之前 .toolchain 验收导出目录可能仅含结果文件，不能直接作为控制区。缺失必要记录返回需要核对，不据此断言数据丢失。

## 使用

Linux 上，在有权读取的既有私有控制区进行观察：

```text
./backup-status-linux status --control-root /absolute/private/control --operation 40000000-0000-4000-8000-000000000001
./backup-status-linux status --control-root /absolute/private/control --operation 40000000-0000-4000-8000-000000000001 --format text
```

示例路径/UUID 是占位值。参数必须分开写；路径为规范绝对路径，不允许未知/重复参数、额外位置参数或环境配置回退。默认输出 JSON，文本模式显示固定中文标签及状态代码。错误不回显私有路径或底层错误。

## 解读

| status | 含义 |
|---|---|
| observed_registered | 此次观察到 registered 记录，报告/版本链自洽且按诊断传入时间尚有效 |
| registration_pending | prepared 可见；registration_chain 指示仍见 base 或已见 next，结果需核对 |
| review_only / records_only | 未见登记或报告，没有自动推进 |
| blocked | 报告过期/未完成、后续操作改变版本链、取消或其他门禁阻断 |
| inconsistent | 控制记录、报告或登记摘要/身份矛盾，或登记存在但报告缺失 |
| changed | 两轮读取中内容或存在性变化，不返回部分成功观察 |
| unavailable | 必要记录缺失、损坏、不可读或输入不满足约束 |

`report_claims` 只描述文件的检查声明，不能证明目标当前状态；`report_freshness` 按传入的诊断基准时间解释，不是随后永久有效。没有重新运行身份、数据或 HTTP 核验。

**persistence_confirmation 始终 not_inferred，ready_for_cutover/deployment_authorized 始终 false。** 磁盘可见的 registered 与“写成功但确认丢失”的文件可能完全相同，status 无法区分历史确认，也不会补发写入权限。

| 返回码 | 含义 |
|---|---|
| 0 | observed_registered，仍只是只读观察，不是允许执行 |
| 2 | 参数错误，尚未打开控制区 |
| 3 | 安全目录/平台能力不可用 |
| 4 | 已输出需要核对的诊断状态 |
| 5 | 输出或目录关闭失败 |

不得把返回码0作为自动恢复/切换条件，也不要因返回4就删除 prepared、覆盖报告或重跑恢复。重复读取不是原子快照/ABA防护，最后读取之后仍可能发生变化。失败后保留原始控制记录，由操作员核对。

## 构建与验收

源码入口 backend/cmd/backup。项目 backend 目录、使用仓库指定 Go1.26.11：

```text
go build -buildvcs=false -mod=readonly -o ../.toolchain/backup-status-linux ./cmd/backup
```

在 Windows 交叉构建时另显式设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0，并使用仓库模块缓存；不表示 Windows 可以原生读取安全控制区。当前沙箱 VCS 自动标记不可用，因此构建显式关闭标记，验收另记录分支/HEAD/工作树与二进制摘要，不改 Git 信任配置。

真实独立进程验证、返回码、只读及链接/权限检查见 [验收记录](backup-status-cli-verification-2026-11-16.md)。本轮未部署或对业务控制区运行命令。
