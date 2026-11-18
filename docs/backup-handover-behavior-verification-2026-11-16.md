# 同次隔离 HTTP 行为与完整审阅报告验收

日期：2026-11-16。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；沿用未提交工作树。未部署、提交、推送或连接业务实例。

## 本轮完成与范围

先编写 [行为桥接内部协议](backup-handover-behavior-design-2026-11-16.md)，新增 handover_behavior.go、单测及隔离夹具适配器，把既有真实 HTTP 验收绑定到**同次恢复操作**的八项草稿，终结为十项全通过的隔离审阅报告。

本模块只接受 isolated-http-fixture-v1 私有授权描述；没有面向真实用户的通用 HTTP 运行器、生产凭据采集/授权入口或 CLI。夹具归档前准备合成账号，不能用来替代真实用户授权或生产能力验收。私有行为凭据不接受外部 JSON，也不是自认证的生产执行凭证。

## 已验证的行为

- 行为写入之前先完成真实身份/数据八项草稿，原 manifest 行数、对象/header、会话撤销及任务安全检查均未放宽。
- 对授权账号核对启用状态、用户名、密码哈希；旧 access 必须源密钥签发、HS256、exp/iat 有效、用户相同且 tv 与恢复后版本恰差一。旧 refresh 必须对应归档中的已撤销且尚未过期会话。HTTP 结束后再核对旧凭据，避免途中到期的 401 被误记为撤销。
- 复用真实 Router/Service 的健康/readiness、旧 access/refresh 拒绝、使用旧 JWT 配置时因 token_version 拒绝、新登录与刷新、用户/书架/目录/本地正文/进度/书签、跨用户拒绝及并发 refresh 重放保护。没有 worker/scheduler，拒绝外部抓取器计数为零。临时 HTTP listener 只在无外网容器内回环，结束关闭。
- 跟踪真实返回的新 refresh token（包括并发验证签发的 token），映射已知新会话 UUID，使用现有 Logout 撤销；不删除会话、不重置恢复用户密码，也不回写原 last_login_at。正常登录产生的 last_login_at 和已撤销新会话行明确保留。
- 关闭 HTTP listener 和连接池后，live guard 再验证对象/header/引用/安全状态。仅 auth_sessions 行数允许原 manifest 数 + 本轮已知唯一 UUID 数；逐个核对授权用户、行为窗口内创建、已撤销，上限 32。其他表仍严格匹配原行数，全部会话无活跃、全部任务无 queued/running。
- 故意漏报一个已知验证会话时，终结返回失败；没有泛化为忽略会话数差异。终结成功不改变原八项草稿，保留原 expires_at，不续期。十项全通过后才排他保存，控制记录未推进。

并发重放防护按既有认证语义可能撤销该用户全部活动会话。本轮隔离目标没有真实流量，所有活动会话来自夹具；不能把这段流程直接用于有用户流量的目标。失败时普通认证副作用不等于可回滚，通用中断清理仍未实现。

## 真实归档恢复结果

最终目录 `.toolchain/backup-restore-db-f4783f1111cc/`，result.json passed=true。4 个顶层/40 个子测试，共 **44 个通过事件，0 失败/跳过**；四个临时容器按标签核对后清理，cleanup 全 true。

- PostgreSQL16.15 与独立 Redis7，使用本地缓存镜像，无外网、无宿主端口、数据 tmpfs；测试 UID10001，源码/文档/二进制/合成归档只读挂载。未读取业务配置。
- 最终 Linux amd64 二进制 SHA-256：`e0aedb81645dffa27dee203dce0e4474f2ea310a813095a91d13940e1daae7b3`。
- 基础合成 dump SHA-256：`3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303`；本轮不同 header/age 包的真实摘要由各自报告绑定，不等于基础 dump 摘要。
- behavior-review.json 的 **10 项均 pass**，operation/plan/package/manifest/target/publication 与同轮 http-evidence.json 逐项一致，均为 headerhttp 同一操作；没有借用另一个 headergood 的八项数据报告。
- ready_for_cutover=false、deployment_authorized=false，operation 仍 validating，未进行状态 CAS。报告只是有时效的隔离审阅结果，不能重新导入为执行授权。
- 新增 6 个行为子测试：八项草稿、坏密码拒绝、授权账号预检、未记账会话拒绝、十项终结、报告保存。之前 38 个事务/header/对象/HTTP 事件及发布确认丢失拒绝断言保留。

首轮 `.toolchain/backup-restore-db-032f1e0df1cf/` 已通过 43 个事件并清理；随后补充未记账会话反例及草稿/有效期不变断言，重建跑最终 44 个事件，不合并两轮计数。

## 单测与工程检查

- Windows Go1.26.11 backup 包 **66 个顶层、321 个含子测试通过事件，0 失败/跳过**。日志 `.toolchain/backup-handover-behavior-windows-20261116.jsonl`。
- 新增 3 个顶层/30 个含子测试通过事件，覆盖旧 JWT 过期、缺失 exp/iat、未来 iat、算法/签名/主体/版本不符、受限会话 UUID/用户/时间窗口、授权 profile 和同操作绑定。
- `go vet -tags=integration ./internal/backup`、gofmt、Python py_compile、git diff --check（core.safecrlf=false）通过。
- `--handover-behavior` 缺少 `--handover-data` 时在创建夹具前拒绝；原无开关 31 个事件、仅数据开关 38 个事件的预期保留。
- 冻结契约 65 项检查通过、16 个摘要匹配；公开 API、SQL DDL、迁移基线和冻结备份结构未改。

本轮未跑 Linux race、完整后端/前端回归或 iPhone Safari 真机，未把历史结果计为新通过。

## 复现与后续

使用仓库 Go1.26.11、GOPATH/GOCACHE，GOTMPDIR 指向项目内目录。在 backend 设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0 后：

```text
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-handover-behavior-linux.test ./internal/backup
```

在项目根运行：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-handover-behavior-linux.test --handover-data --handover-behavior
```

脚本只创建自有合成夹具，不接受业务 DSN。报告保存不等于报告/operation 的原子登记；多次读取、协作租约和时点身份均不能阻止管理员或最后读后的非协作变更。tmpfs 不等于真实 NAS/断电持久性。真实用户凭据和行为运行器、持久 CAS、只读/受控执行 CLI、实际目标交接及真机验收仍待完成，未达到正式发布或完整备份恢复生产闭环。
