# 动作账本只读CLI验收

## 交付范围

依据先行 [CLI契约](backup-behavior-status-cli-design-2026-11-16.md)，新增 `backup behavior-status --control-root ABS --operation UUID [--format json|text]`，复用DiagnoseBehaviorLedger。与status共享严格参数/安全目录打开，输出前检查Close成功；旧status有效输出结构与退出语义保留。

JSON提供已有纯记录聚合观察，text明确“数据库撤销：未核验；历史持久确认：无法推断；允许派发/重试/部署：否”。0只表示responses_observed，4为需核对状态；不读取DSN/密码/keyring/归档，不接受连接或重试参数，不连接数据库/Redis/HTTP，不执行登录、撤销、恢复、修复或清理。Windows安全驱动未支持时拒绝，不回退普通文件读取。

已补 [操作员说明](backup-behavior-status-cli-2026-11-16.md)，解释正确原控制区、状态和退出码、未知结果的保留原则及与status/inspect/plan的边界。单独报告目录不是控制区，输出不证明会话撤销或历史确认。

## 验证

- 新增cmd/backup 3个顶层/2个子测试（5个通过事件）：JSON/text完整链、孤立logout的注意状态、重复/非法参数/额外DSN拒绝、打开/关闭/输出错误脱敏、Windows原生能力不足拒绝。
- Windows Go1.26.11：backup 98个顶层/503个通过事件；cmd/backup 8个顶层/12个通过事件，无失败/跳过。日志 `.toolchain/backup-behavior-status-unit.jsonl`，不表示全仓库回归。
- Linux amd64独立进程：新命令8项子场景、旧status 7项子场景，连同2个顶层共17个通过事件。新场景包括JSON/text、缺意向且有孤立动作、父response未确认、父摘要矛盾、过期、符号链接根及不安全权限拒绝。命令前后控制区文件内容/权限一致；反例由夹具显式改变并恢复，非命令写入。
- go vet（integration标签）、gofmt、Python编译、git diff --check通过；65项契约检查、16个冻结文件摘要匹配。未改API、DDL、账本格式或原诊断算法。

最终 `.toolchain/backup-behavior-status-cli-881cbf488607/result.json` passed=true、cleanup=true。evidence.json分别记录新命令checks=8与旧status checks=7，所有派发/重试/切换/部署许可均false。

| 产物 | SHA256 |
|---|---|
| backup-behavior-status-linux | c79240a6ad8460ff5e796e006b2c6718e39fa5e51cd32c0ab757624f6e0b22da |
| backup-behavior-status-cli-linux.test | d922db59114317287f9ea065196b661af284995ed1320f4af48a37e51ee703d5 |
| Linux运行镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

脚本 scripts/verify-backup-behavior-status-cli.py；唯一自有容器network=none、UID10001、只读根、cap-drop ALL/no-new-privileges，CPU/内存/PID限额；源码/文档/二进制只读挂载，夹具只用tmpfs。仅借用缓存Redis镜像作为Linux运行环境，没有启动Redis、数据库、应用服务或宿主端口；注入的假DATABASE_URL/REDIS_URL未用于命令连接。容器已清理。

本轮没有重跑上一轮98事件的完整数据库恢复链；数据库撤销观察证据仍见 [上一轮验收](backup-behavior-ledger-verification-2026-11-16.md)，不能与本轮17事件相加当作同次完整回归。此次夹具的账号/报告/动作仅用于构造文件状态，不证明实际登录或数据库恢复。

## 下一步

只读CLI已能展示动作链；下一步明确受限HTTP运输层的目标绑定/禁止重定向重试及会话结果接口，再结合真实同次行为复核设计通用报告。当前仍只有内部派发内核与测试AuthService适配器，没有生产HTTP/执行CLI或真实用户交接，不能放宽既有合成profile或从磁盘恢复执行许可。

本轮未部署、提交、推送、合并或重新生成Gemini审计包；ARM/NAS和iPhone真机等待验项保持原状。分支codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。
