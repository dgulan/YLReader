# 运行配置装配与 Redis 身份核验验收

日期：2026-11-16。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。沿用已有未提交工作树，没有部署、提交、推送或业务缓存/数据库访问。

## 已实现范围

依据 [先行内部协议](backup-runtime-redis-design-2026-11-16.md)，新增 backup/runtime_identity.go、redis_identity.go、对应单测/隔离集成测试，以及 cache/redis_identity.go 专用受限只读协议和测试。没有新增依赖或公开 API。

运行配置核验显式装配的 target/config/JWT/keyring 修订 UUID、实际 JWT 字节和目标 keyring；拒绝相同 JWT、长度错误、元数据漂移、缺失/重复 KID 和当前 KID 不符。临时随机挑战验证当前 keyring 的 Seal/Open，摘要仅绑定非敏感元数据与所需 KID 列表，不对 JWT 或 header 密钥做哈希，不修改调用方秘密或全局 keyring。

Redis 入口仅支持显式回环字面 IP/端口/逻辑库、独立预登记 run_id 和预置的操作专属见证。每次观察在同一连接执行 INFO server / INFO replication / GET / INFO server / INFO replication，核对 standalone、master、无已连接副本及前后相同 run_id；源目标再各读两轮。拒绝同进程不同逻辑库、身份或见证漂移、见证缺失、ACL 拒绝、异常/超限 RESP 和取消。所有错误脱敏。

探测没有 SET/SETEX/DEL/FLUSH/KEYS/EVAL 或 TTL 刷新；预置见证只由自有测试夹具进行。只输出目标 run_id/逻辑库的身份摘要，不输出原始 INFO、URL、密码或见证值。两份摘要可用作 handoverTargetIdentity 的 Runtime/Redis 输入，但尚未自动生成报告或推进状态。

## 真实 Redis 隔离验收

最终目录：`.toolchain/backup-redis-identity-19917dcd9bda/`，result.json passed=true；1 个顶层、11 个子测试，共 12 个通过事件，0 失败/跳过。

- 本地缓存 redis:7-alpine 镜像 `sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b`，没有拉取镜像。
- 最终 Linux amd64 测试二进制 SHA-256：`509889fe374a79286c54fe83ceafa1545f89de35774a476a7eac019246de7f6e`。
- 两个独立进程，共享本轮 network=none 命名空间的不同回环端口，无宿主端口、无持久数据卷、数据 tmpfs、只读根、CPU/内存/PID 限制。测试容器 UID10001、cap-drop=ALL、no-new-privileges，只挂载只读测试二进制，不挂业务配置。
- 成功只读探测，拒绝 endpoint 别名、同进程不同库、run_id/逻辑库漂移、错误/相同/缺失见证、ACL INFO 权限不足及取消。
- 两端原见证和中文/emoji 哨兵键仍存在且值未变，目标见证 TTL 未增加；两个 Redis 无 OOM 或重启。三个容器均经本轮标签核对后清理，cleanup 全 true。
- evidence.json scope=isolated-redis-identity-fixture，witnesses_and_sentinels_retained=true，deployment_authorized/ready_for_cutover=false。

前一轮 `.toolchain/backup-redis-identity-29b96dcf248e/` 也通过并清理。复核后增加 standalone/master/无副本限制，再重建运行上述最终轮；两轮不是重复计入的新测试数量。集群/副本角色反例由缓存协议单测覆盖，未以真实复制集群演练冒充验证。

## 单测及工程检查

- Windows Go1.26.11：backup 包 55 个顶层、260 个含子测试通过事件，0 失败/跳过。日志 `.toolchain/backup-runtime-redis-windows-20261116.jsonl` 中 backup 包结果。
- 最终 cache 包 7 个顶层、35 个含子测试通过事件，0 失败，另有 2 个既有真实 Redis 用例因未提供 TEST_REDIS_ADDR 跳过；日志 `.toolchain/backup-runtime-redis-cache-final-20261116.jsonl`。新增协议单测核对实际命令白名单、AUTH/SELECT、长度及 CRLF、缺失/重复 run_id、角色限制和身份漂移。不能把新增恢复夹具说成旧限流集成用例已运行。
- 首轮受限运行的 cache 包回环测试失败并阻塞，停止该次命令；原日志保留，不计通过。随后允许本机连接的重试曾因系统 Temp 创建失败未进入测试；改用项目内 GOTMPDIR/TMP/TEMP 后，设置 60 秒上限通过。`.toolchain/backup-runtime-redis-cache-retry2-20261116.jsonl` 是角色限制补强前的通过记录，最终以 cache-final 为准。
- `go vet -tags=integration ./internal/backup ./internal/pkg/cache`、gofmt、Python py_compile、git diff --check（core.safecrlf=false）通过。
- 冻结契约检查 65 项通过、16 个摘要匹配。未修改公开 API、DDL、迁移基线、冻结备份格式或 operation 状态。

## 复现与限制

使用本地 Go1.26.11、仓库 GOPATH/GOCACHE，必要时将 GOTMPDIR/TMP/TEMP 显式设为项目内临时目录。backend 目录设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0 后：

```text
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-redis-identity-linux.test ./internal/backup
```

在项目根运行 `python scripts/verify-backup-redis-identity.py`。脚本不接受业务 URL，只用本地缓存镜像创建全新夹具并清理。本轮未跑 Linux race、完整后端/前端回归或真实归档恢复全链。

运行配置目前是显式输入的装配验证，不是正在运行服务的配置证明；修订标识不可信、秘密变化未更新修订、JWT 熵不足及恢复 header 的全部正确性不能由本模块推断。真实安全配置装载和现有全量 header 门禁还需接入完整链。

Redis run_id 和双向见证属于时点观察；预登记、见证可信来源及外部隔离是前提，不能阻止观察后新建复制关系、代理伪造、重启或非协作改动。只读 GET 会影响访问统计，未宣称 Redis 所有内部状态逐位不变。后续将数据库/运行配置/Redis 探测与对象/header 验证编排进正式报告，再完成报告/state 持久 CAS 和只读 CLI；本轮没有放行恢复或服务切换。iPhone Safari 真机验收仍未执行。
