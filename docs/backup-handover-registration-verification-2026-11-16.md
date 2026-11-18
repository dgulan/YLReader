# 隔离报告版本 CAS 登记验收

日期：2026-11-16。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；沿用未提交工作树。未部署、提交、推送、切换服务或访问业务实例。

## 完成范围

依据 [先行登记协议](backup-handover-registration-design-2026-11-16.md)，新增严格登记结构、排他 prepared、操作原字节 SHA CAS、registered 确认链和 live guard 适配。**这是隔离审阅报告的版本登记，不是 ready_for_cutover/completed 或生产发布放行。**

同次行为终结保留私有最终报告摘要与已知会话增量。只有 guard 报告保存方法明确成功并回读确认后，才授予 persisted 内存能力；报告已落盘但确认丢失时，不能通过读取 JSON 恢复权限。保存与登记尝试均一次性锁存，不自动重试。Close 清除活体证明。

登记前实际重跑数据库/配置/Redis 身份探测，与报告摘要一致，并按同次会话增量重验对象/header/安全状态。登记先保存 prepared，再以旧 state 原字节 SHA 比较交换，保留 validating，仅 generation+1、更新心跳/更新时间；回读后对 prepared 执行 CAS 为 registered，再确认报告、操作及 attempt/publication 均匹配。

报告本身不修改，其 observed_generation 仍指旧版本；登记记录明确连接旧报告摘要、旧 state SHA/版本与新 state SHA/版本。旧报告不会被谎称直接绑定新版本，也不能通过旧 validateFor 直接重用到新 generation。

任何确认丢失都保留文件。state CAS 前的明确冲突可返回冲突；state 已成功后，即使登记 CAS 是明确冲突，也返回结果未知。不会回滚/删除、自动续跑或仅凭磁盘 registered 补发成功。

## 真实恢复与 Linux 文件验证

最终目录 `.toolchain/backup-restore-db-312f8dad75ea/`，result.json passed=true。**5 个顶层/47 个子测试，共 52 个通过事件，0 失败/跳过**。四个自有临时容器按标签清理，cleanup 全 true。

- 保留真实 pg_dump/age、事务/header/对象/HTTP 和十项报告验收。Linux 安全文件 tmpfs 另验证登记成功后关闭重开回读、并发单胜者、state CAS 实际成功后确认丢失、登记 CAS 实际成功后确认丢失、陈旧报告提前拒绝。
- headerhttp 同次 live guard 在十项报告明确保存之后，重新采集身份/数据再登记；**真实操作版本 4→5，阶段仍 validating**。重复调用返回拒绝且新版本/state 摘要不变。
- review-registration.json 为 registered；报告摘要与 Go 原始报告字节 SHA 一致，base state SHA/版本与 behavior-review.json 的原 snapshot 一致，next state SHA/版本由实际 Control 回读确认。ready_for_cutover/deployment_authorized 均 false。
- 本地缓存 PostgreSQL16.15 / Redis7，数据 tmpfs、无外网、无宿主端口；测试 UID10001，只读挂载二进制/源码/文档/合成归档，不读业务配置。
- Linux amd64 二进制 SHA-256：`a06eaeb80e6f27455a21760b4c8640033d6fe3ad330e67b2df12fc53137299a6`；基础合成 dump SHA-256：`3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303`。具体 age 包由本轮生成并由报告绑定。

文件故障测试使用合成记录验证存储协议；同次真实恢复验证使用真实报告和 live guard，不混同二者。tmpfs 回读不能证明断电/真实 NAS 的持久性。

## 单测与工程检查

- Windows Go1.26.11 backup 包：**72 个顶层、336 个含子测试通过事件，0 失败/跳过**，日志 `.toolchain/backup-handover-registration-windows-20261116.jsonl`。
- 新增 6 个顶层/15 个含子测试事件，覆盖版本链/阶段保持、prepared/state/receipt 确认丢失、登记冲突后不误报无副作用、回读错误、严格 JSON、陈旧/部分/改动报告、并发单胜者，以及磁盘可见报告不能恢复丢失确认的能力。
- `go vet -tags=integration ./internal/backup`、gofmt、Python py_compile、git diff --check（core.safecrlf=false）通过。
- `--handover-register` 缺少行为开关时在创建夹具之前拒绝；原默认/数据/行为模式分别保留 31/38/44 个事件的预期。
- 冻结契约 65 项检查通过、16 个摘要匹配。未改公开 API、SQL DDL、迁移基线或冻结 report/operation 字段；新内部登记文件单独定义。

本轮未运行 Linux race、完整后端/前端回归或 iPhone Safari 真机，未把历史项目计作新通过。

## 复现和剩余边界

使用仓库 Go1.26.11、GOPATH/GOCACHE、项目内 GOTMPDIR。在 backend 设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0：

```text
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-handover-registration-linux.test ./internal/backup
```

项目根运行：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-handover-registration-linux.test --handover-data --handover-behavior --handover-register
```

本轮实际使用默认合成归档；脚本不接受业务 DSN。新增 registered 只是隔离审阅日志确认，operation 仍 validating，不是服务切换授权。

多文件不是原子事务，state CAS 是版本比较点；prepared/registered 与后续读是确认协议，不是跨文件原子快照、ABA 防护或最后读后永久保证。实际数据/身份改变、后续心跳版本变化、进程中断和确认丢失仍需只读核对，不能通过重建 guard 自动接管。

通用真实用户授权/凭据运行器、只读/受控执行 CLI、实际目标交接、NAS/断电持久性及真机验收仍未闭环。下一步把登记链与异常状态接入只读诊断/命令输出，再推进通用运行器与受控执行边界；不能凭本轮隔离报告放行生产恢复或正式发布。
