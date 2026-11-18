# 恢复记录只读诊断验收

日期：2026-11-16。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；本轮沿用已有未提交工作树，无部署、提交、推送或业务数据库操作。

## 本轮完成

按 [先行内部协议](backup-diagnostics-design-2026-11-16.md) 新增 backend/internal/backup/diagnostics.go 与 diagnostics_test.go。DiagnoseRestoreRecords 仅接受 Get 能力，复用现有严格记录解析器，交叉核对 state/attempt/publication 的操作、备份、计划、所有者、目标存储及暂存 generation。两轮记录摘要变化输出 changed；数据库 unknown 等输出 blocked；缺失、畸形、身份冲突分别输出固定原因。

输出不包含底层错误、DSN、密钥、正文、宿主路径或对象 key。deployment_authorized / ready_for_cutover 固定为 false。即使三份记录自洽且分别记载 committed / verified，consistent_records 也仅代表记录观察结果，不是数据库提交证明、租约、计划有效期证明或正式交接凭据。没有公开 API、CLI、自动接管、重试恢复、清理或状态推进。

## 验证

使用本地 Go 1.26.11，GOTOOLCHAIN=local，仓库内 GOPATH/GOCACHE：

- `go test -count=1 ./internal/backup`：通过。新增 6 个顶层诊断测试，覆盖只读与脱敏、7 类阻断、5 类绑定漂移、6 类缺失/畸形/错误、5 类观察变化、非法输入及写适配器拒绝。
- `go vet ./internal/backup`：通过。
- `python scripts/verify-backup-contract.py`：65 项检查通过、16 个冻结摘要一致。
- `git diff --check`（core.safecrlf=false）：通过。

本轮为纯模型和静态验证，测试使用仅有 Get 的内存存储，不连接真实 PostgreSQL/Redis 或安全文件驱动；未运行 Linux race、完整后端集成测试、前端回归或恢复容器演练，不计为这些验收通过。重复读不能检测 ABA，也不是原子快照；最终读后记录仍可能变化。

## 下一步

补齐正式目标配置与身份核验协议，确定正式交接报告与持久登记边界，再接入只读 CLI；执行入口继续复用既有守卫链并单独进行隔离故障验收。当前诊断模块不能用来放行 ready_for_cutover/completed。iPhone Safari 真机验收仍未执行，第 3 阶段没有因此完成。
