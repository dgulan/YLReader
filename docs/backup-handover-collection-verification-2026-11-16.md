# 交接报告采集与保存验收

日期：2026-11-16。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；沿用已有未提交工作树，未部署、提交、推送或访问业务实例。

## 本轮完成

先编写 [采集编排内部协议](backup-handover-collection-design-2026-11-16.md)，再新增 handover_collection.go、单测、Linux 隔离集成测试和 verify-backup-handover-collection.py。生产适配器实际调用此前的数据库、运行配置装配和 Redis 探测；校验数据库 DSN 的 host/port/database 及运行配置目标与 Plan 一致，不接受外部 JSON 提供 pass 结果。

采集前、采集后、保存前及保存后严格核对 operation/attempt/publication 的原字节摘要与计划绑定。计划过期、取消、未知数据库结果、未确认 publication、探测错误、畸形摘要、记录变化或逆行时钟均拒绝。报告有效期不超过五分钟或原计划到期时间，两者取早；不续期计划。

复用既有排他报告保存及回读核验，确认丢失时保留报告并返回 unconfirmed；保存之后记录变化或过期也返回失败并保留报告，不描述为回滚。state.json 没有写入或状态推进。

共同绑定检查与全检查通过门禁已分开：实际探测支持 schema_ledger / database_identity / database_role / redis_isolation 四项 pass；published_objects / header_keyring / jwt_rotation / sessions_revoked / tasks_disabled / runtime_behavior 六项固定 unavailable。内存 keyring/JWT 装配自检没有被冒充为全量 header 或旧令牌拒绝。**该部分报告不能通过原 validateFor 的全通过门禁，ready_for_cutover 与 deployment_authorized 都为 false。**

## 真实联合隔离验证

目录：`.toolchain/backup-handover-collection-991ed5e8856e/`。result.json passed=true，1 个顶层/3 个子测试，共 4 个通过事件；0 失败、0 跳过。

- PostgreSQL16 和两个独立 Redis7，复用本地缓存镜像，未拉取；无外网、无宿主端口、数据 tmpfs。测试 UID10001、只读根、cap-drop=ALL、no-new-privileges；源码/文档/二进制只读挂载，不挂业务配置。
- Linux amd64 测试二进制 SHA-256：`923118f597dceb59dc70bf71741a37a9cb8609a5f06de78f0c017e4f9f410563`。
- 实际执行三类生产探测适配器，以本轮真实 UTC 微秒时间创建新计划并记录采集时间；生成报告 4 项 pass / 6 项 unavailable。真实探测的数据库是本轮建库及可信 baseline，Redis 见证由本轮 fixture 预置。
- 通过安全文件驱动在私有 tmpfs 控制区排他保存报告，关闭重开后严格解析回读；原 state/attempt/publication 三份摘要和 operation generation 保持不变。显式断言部分报告不能通过全通过门禁。
- PostgreSQL 和两个 Redis 均无 OOM 或重启；四个本轮容器按标签核对后清理，cleanup 全 true。

report.json 是本轮采集记录，不是执行凭证；其五分钟有效期过后不能重用于新操作。控制记录、package/manifest/storage/publication 声明来自合成模型，部分摘要含固定样例值；本轮没有真实归档恢复、对象发布、header 认证或 HTTP 行为验证，不把合成 verified/committed 记录冒充真实提交证据。tmpfs 重开回读也不代表断电持久性或真实 NAS 验证。

## 单测与工程检查

- Windows Go1.26.11：backup 包 60 个顶层、283 个含子测试通过事件，0 失败/跳过。最终日志 `.toolchain/backup-handover-collection-windows-final-20261116.jsonl`。
- 本轮新增 5 个顶层、23 个含子测试通过事件；覆盖部分报告门禁、探测错误/取消/畸形摘要、计划过期/绑定漂移、记录变化、时钟逆行、TTL 上限、保存前/后变化、保存后过期和确认丢失。
- `go vet -tags=integration ./internal/backup`、gofmt、Python py_compile、git diff --check（core.safecrlf=false）通过。
- 冻结契约 65 项检查通过、16 个摘要一致；公开 API、SQL DDL、迁移基线和冻结备份结构未变。

本轮未重跑 Linux race、完整后端/前端回归或此前的真实归档/HTTP 恢复链，未将历史结果计作新验证。

## 复现和下一步

使用仓库 Go1.26.11、GOPATH/GOCACHE，GOTMPDIR 指向项目内临时目录。在 backend 目录设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0，运行：

```text
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-handover-collection-linux.test ./internal/backup
```

回到项目根运行 `python scripts/verify-backup-handover-collection.py`。该脚本不接受业务 DSN，只使用缓存镜像创建全新夹具。

下一步接入既有对象/header/会话/任务及授权运行行为核验，减少 unavailable 项，再在真实归档恢复链上验证报告。报告与 operation 的持久 CAS 登记仍未实现；目前多次读取不是原子快照、租约或 ABA 防护，不能保证最后读后状态不变，不会放行 ready_for_cutover/completed。只读 CLI、真实配置装载与 iPhone Safari 真机验收也仍待完成。
