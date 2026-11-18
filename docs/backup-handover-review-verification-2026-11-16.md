# 交接审阅报告模型与保存验收

日期：2026-11-16。分支 codex/pre-release-20261113；HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。沿用已有未提交工作树；没有部署、提交、推送或业务数据库操作。

## 已实现范围

先编写 [内部协议 v1](backup-handover-review-design-2026-11-16.md)，再实现 backend/internal/backup/handover_review.go 与 handover_review_test.go。报告绑定 plan/package/manifest/target/publication、三份控制记录原字节摘要和操作 generation；数据库身份、运行配置修订及 Redis 身份各有独立的非敏感摘要槽位。

严格解析全部字段及十项固定检查集合，拒绝重复、未知、缺失/null 字段、超限输入和任意状态。检查窗口、报告有效期分别最多五分钟；目标绑定校验同时拒绝过期、未到完成时间、早于计划/操作更新时间、超过既有计划到期时间以及检查未全通过。不会通过新报告延长既有 15 分钟计划。

报告仅支持安全 ConditionalFiles 的 CreateOnly 排他保存，成功后回读并比较原始字节摘要。已有报告不可覆盖；写入确认丢失、回读失败、字节不符都返回固定 unconfirmed，保留文件。存储同时返回冲突与 ErrCommitUnknown 时仍按 unknown 处理，不能误降为普通冲突。失败/不可用检查的报告允许保留供审阅。

**真实目标探测器尚未实现。** 非敏感身份摘要与检查结果当前由调用方输入；格式、绑定及保存成功不证明检查真实发生。本模块为未接入执行链的内部模型，没有公开 CLI/API；deployment_authorized / ready_for_cutover 固定 false，未修改 state.json。报告与操作状态的跨文件 CAS 登记、目标身份探测、安全驱动持久性及真实恢复链还需另验。

## 本轮结果

本地 Go 1.26.11，GOTOOLCHAIN=local，使用仓库内 GOPATH/GOCACHE：

- `go test -json -count=1 ./internal/backup`：49 个顶层、245 个含子测试通过事件；0 失败、0 跳过。其中新增报告测试为 6 个顶层、57 个含子测试通过事件。日志：`.toolchain/backup-handover-review-windows-20261116.jsonl`。
- `go vet ./internal/backup`：通过。
- `python scripts/verify-backup-contract.py`：65 项检查通过、16 个冻结摘要一致。
- gofmt 与 `git diff --check`（core.safecrlf=false）：通过。

覆盖格式及检查集合、计划/记录/运行身份漂移、取消、unknown、publication 未确认、时间边界、并发创建、确认丢失与保留。纯模型和线程安全内存存储验证不等于真实 LocalFS 文件持久性或数据库/Redis 身份探测；本轮未运行 Linux race、恢复容器演练、前端回归或完整后端集成测试。

## 后续

按先行协议接入只读数据库/运行配置/Redis 身份探测器，明确源目标隔离与权限不足的拒绝行为；在隔离目标重新核对物理身份和安全设置。之后再设计报告与 operation generation 的持久 CAS 登记、只读 CLI 及受控执行链。现有报告不具备放行能力，也不能用于宣称生产交接或第 3 阶段真机验收完成。
