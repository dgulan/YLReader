# 登录前动作意向登记与结果未知诊断验收

## 本轮结果

依据先行 [内部契约](backup-behavior-intent-design-2026-11-16.md) 实现 backend/internal/backup/behavior_intent.go：每个 operation 唯一 create-only 意向槽，登记前后核对预检输入、稳定控制记录、版本/目标/运行修订和时效。仅保存 UUID 与非秘密绑定；不保存用户名、凭据、凭据路径或秘密摘要。确认丢失/回读失败/写后变化保留文件，不返回成功，不自动重试。

DiagnoseBehaviorIntent 使用只读接口做两轮观察，区分未观察到意向、结果未知、陈旧、绑定矛盾、读取中变化及不可用。只要看到一致 dispatch_reserved，也不能推断请求已经发送或会话是否创建。persistence_confirmation 始终 not_inferred，dispatch_authorized/retry_authorized/deployment_authorized 始终 false。

此处只有内部登记核心与诊断函数，没有生产调用入口、派发许可或 HTTP 登录；现有 status CLI 没有新增行为意向展示字段/子命令。不能从磁盘重建执行凭证。当前槽的防重放限于保留的同一控制区/operation；外部移除或替换卷不能由它独自解决，后续 live guard 仍须检查物理身份。

## 验证结果

- 新增5个顶层/25个子测试，共30个通过事件。覆盖唯一槽、更换consent仍冲突、16路并发单胜者、写错误、写确认丢失、回读失败、取消、版本漂移、写后过期、前置条件拒绝、畸形/过大JSON、诊断变化/权限错误及只读不变性。
- Go1.26.11 Windows：backup 88个顶层/448个通过事件，cmd/backup 5个顶层/7个通过事件，无失败/跳过。日志 `.toolchain/backup-intent-unit.jsonl`；不是全仓库或完整HTTP恢复链回归。
- Linux amd64 安全文件驱动：1个顶层+4个子测试=5个通过事件。持久登记后关闭重开仍拒绝重复登记；8路并发只有一个成功；真实文件写入确认丢失和回读失败均保留且重开后不放行；原控制记录内容与权限未变。新增意向文件是本轮预期写入，不能将整个控制区称为未变化。
- go vet（integration标签）、gofmt、Python编译及git diff --check通过；65项契约检查、16个冻结文件摘要匹配。

脚本 scripts/verify-backup-behavior-intent.py；结果 `.toolchain/backup-intent-af87b9caf51a/result.json` passed=true、cleanup=true。二进制 `.toolchain/backup-intent-linux.test` SHA256：`e6e93d68c6f7ebc5f3b21cbc6f73c07961d2517b24d5a9963837f51066edc97b`。缓存 Linux 运行镜像 SHA256：`c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b`。

仅本轮自有 network-none/UID10001/只读根容器；源码、文档、二进制只读挂载，意向和控制夹具仅tmpfs，cap-drop ALL/no-new-privileges且有CPU/内存/PID限额。没有应用、数据库或Redis服务、对外端口和实际登录；容器已清理。

## 后续边界

下一步在live guard下核验真实账号/旧JWT及refresh、绑定独立目标，再将登记核心接到一次性派发及已知会话结果记账。需要区分“登记成功确认”“请求已开始但响应未知”“已知会话UUID及登出结果”，禁止中断重试登录。通用用户报告、受控恢复执行、实际交接仍未完成，不推进ready_for_cutover/completed。

本轮未部署、提交、推送、合并或对业务库演练；未重新生成Gemini审计包，之前包保持原快照。
