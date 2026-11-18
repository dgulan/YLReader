# 真实恢复数据交接核验验收

日期：2026-11-16。沿用 codex/pre-release-20261113 的既有未提交工作树；未部署、提交、推送或连接业务实例。

## 已完成

依据 [先行内部协议](backup-handover-data-design-2026-11-16.md)，在同次 live restoreGuard 上新增只读数据核验，接入可选 handoverProbes.Data。必须持有有效租约、原验证包/暂存、header 解密证明及本 guard 明确成功返回的 publication 确认；不能从磁盘 JSON 重新获得此能力。

对象发布现在只在 verified 状态更新明确成功后设置 publicationConfirmed 内存位，Close 清除；确认丢失或只读重放看到 verified 不补设。新增检查不会重放发布、覆盖目标、推进状态或清理数据。

核验重读安全已发布根的实际树身份，逐对象按认证 manifest 校验 ID/key/bytes/SHA，复用现有数据库只读校验检查账本、行数、header 全量认证及 KID inventory、实体/对象引用、会话已撤销、任务无 queued/running。完成后重验租约/路径身份、树身份及三份控制记录摘要，只返回与本次 snapshot 绑定的内部证明。

成功后报告新增 published_objects/header_keyring/sessions_revoked/tasks_disabled 四项 pass，变为 **8 pass / 2 unavailable**；jwt_rotation/runtime_behavior 仍 unavailable。默认不配置 Data 的旧采集路径继续 4 pass / 6 unavailable，十项全通过门禁未降低。operation CAS 与 ready_for_cutover/completed 仍关闭。

## 真实归档恢复验证

最终目录 `.toolchain/backup-restore-db-f65f86f5c75a/`，result.json passed=true。真实 pg_dump/age、数据库事务、header、对象发布和既有 HTTP 回归均保留；4 个顶层/34 个子测试，共 38 个通过事件，0 失败/跳过。

- PostgreSQL16.15 + 两个独立 Redis7，复用本地缓存镜像，数据 tmpfs、无宿主端口、无外网；测试 UID10001、只读挂载二进制/源码/文档和合成归档。不读取业务配置。
- Linux amd64 二进制 SHA-256：`a0549f93cc9c80bd188ef8f6ec347da18290819139a47b6cfa50638537bedd8e`；基础合成 dump SHA-256：`3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303`。具体 header 归档及 age 包在夹具内生成，其摘要由本次 report 绑定，不等于基础 dump 摘要。
- 在 headergood 同次 guard、真实提交和对象发布成功之后、任何 HTTP 新登录之前，实际运行数据库/运行配置/Redis/Data 适配器；安全控制区保存 data-review.json 对应报告。报告绑定本次真实 plan/package/manifest/publication 和三份记录摘要，8 项 pass、2 项 unavailable，ready_for_cutover/deployment_authorized 均 false。
- 新增 7 个子测试：live 数据核验成功、错误 keyring、坏对象、坏 header、重新激活会话、重新排队任务、八项报告采集保存。拒绝后坏字节/坏 header/活跃会话/排队任务仍保持测试注入状态，证明没有自动修复；仅测试夹具恢复原值。
- 既有 headerpuback 失败窗口另加断言：即使 verified 已落盘且可只读重放，丢失发布确认的 guard 仍不能授予数据证明。未把这种回读说成持久确认。
- report 保存前后三份控制记录摘要不变；JWT 和 HTTP 行为的全通过门禁依然拒绝部分报告。旧 HTTP 证据来自本轮另一个 headerhttp 操作，不被挪用为 headergood 报告的剩余两项检查。
- 四个自有容器按标签核对后清理，cleanup 全 true；结果、报告、日志和合成故障归档保存在独立 .toolchain 目录，实际恢复目标仅存在本轮容器 tmpfs。未对业务库回滚或清理。

## 单测与工程检查

- Windows Go1.26.11 backup 包：63 个顶层、291 个含子测试通过事件，0 失败/跳过。日志 `.toolchain/backup-handover-data-windows-20261116.jsonl`。新增 3 个顶层/8 个含子测试事件，覆盖内部证明绑定/漂移、失败及缺失 live guard 拒绝。
- `go vet -tags=integration ./internal/backup`、gofmt、Python py_compile 和 git diff --check（core.safecrlf=false）通过。
- 冻结契约 65 项检查通过，16 个冻结摘要一致。未改公开 API、SQL DDL、迁移基线或冻结备份结构。

本轮未运行 Linux race、完整后端单测或前端/真机回归；没有将历史这些项目计作新通过。

## 复现与边界

使用仓库 Go1.26.11、GOPATH/GOCACHE，GOTMPDIR 为项目内临时目录。在 backend 目录设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0 后：

```text
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-handover-data-linux.test ./internal/backup
```

项目根运行 `python scripts/verify-backup-restore-db.py --binary .toolchain/backup-handover-data-linux.test --handover-data`。新增选项为显式 opt-in，未开启时原有 31 个事件及输出要求保持不变。本轮使用默认既有合成 fixture；脚本不接受业务 DSN。

此核验在恢复后首次新登录前执行，manifest 行数包括原归档会话；后续 HTTP 登录会改变会话数，不能把本门禁直接套在行为测试之后。对象检查不枚举额外文件，账本匹配不代表完整 DDL 审计；协作租约和逐对象读取不能排除恶意管理员或最后读后的非协作变更。tmpfs 验证不等于真实 NAS/断电持久性验证。

下一步在同次恢复操作内接入明确授权的 JWT 旧令牌拒绝及运行行为核验，并解决其与会话撤销/行数检查的先后顺序；之后再完成持久 CAS 登记、CLI 和目标交接。真实 iPhone Safari 验收仍未执行。
