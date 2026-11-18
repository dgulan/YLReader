# 旧 refresh_token 受限 HTTPS 拒绝探测与副作用隔离验收（H1）

## 本轮范围

依据先行设计 [backup-old-refresh-design.md](backup-old-refresh-design.md)，在 `backend/internal/backup/behavior_http.go` 中实现受限 HTTPS 运输层的显式内部方法 `rejectOldRefresh(ctx, proof, user, refresh)` 及副作用隔离核验：

- **身份与白名单绑定**：要求非空且未关闭的 `verifiedBehaviorAccounts` 与 live `restoreGuard`（核对 `plan` 摘要、`TargetID`、`database`/`runtime` 指纹、`source`/`target` pin 分离、20s 时效窗口与 `expires`、白名单集合与 `t.users` 严格对齐；当控制文件句柄存在时前后核对 `stableHandoverSnapshot`）。
- **执行顺序与数据库副作用隔离**：限制仅在目标新登录会话创建之前（无 `loginTried` 且 `known` 为空）或全部已尝试新登录均已确认 `Logout` 撤销之后（`known` 为空且每个 `loginTried` 用户均在 `completed`）执行；探测前后分别通过只读 `REPEATABLE READ` 事务（`accountObservationPolicy(..., false)`）核验目标用户未禁用、旧 `refresh_token` 哈希归属该用户且处于已撤销且未过期状态、`auth_sessions` 无活跃会话（`activeForUser == 0` 且 `revoked == total`），并按 `id` 排序计算全表 `(id, user_id, revoked_at)` SHA-256 摘要，确保既有 `revoked_at` 时间戳零篡改、未新增或删除任何会话行。
- **受限 HTTPS 与单次尝试**：复用固定证书 pin、loopback 与禁代理/重定向/连接复用客户端；`t.post` 仅允许 `/api/v1/auth/refresh` 匹配无 `Authorization` 头、`401` 状态与 `<= 8192` 字节 `application/json` 响应；校验严格 UTF-8 与冻结 JSON `Error` 结构（`error.code == "unauthorized"`）；每用户仅锁存一次尝试（前置本地/时序核验通过后立即锁存 `oldRefreshTried[user] = true`），网络/关闭/格式/后置 DB 失败一律判为未确认且禁止重放；`Close()` 清除 `oldRefreshTried` 与内存密钥状态。

本轮不修改 43 个公开 API、`docs/schema.sql`、备份格式、16 份冻结契约摘要或 `bound-behavior-review` 报告结构（仍保持 `partial` / `10 pass, 2 unavailable`）；未接入 `confirmedBehaviorRun` 见证或 CAS 登记（留待 H2/H4）。

## 验证结果

1. **契约检查**：`python scripts/verify-backup-contract.py` 通过（65 项契约检查通过，16 份冻结契约摘要匹配，报告 `.toolchain/backup-contract-checks.json`）。
2. **Windows 本机单测与静态检查**：
   - `behavior_http_test.go` 新增 `TestBehaviorOldRefreshBoundedObservation`（37 个子场景：登录前 `good`、全部登出后 `after_logout`、`200`/`403`/重定向/网络错误/gzip 压缩/超长响应/重复 JSON 键/错误码/错误结构/体关闭失败、空或过期/漂移/关闭的 `proof` 与 `guard`、白名单不匹配、活跃会话及未确认登录阻断、前置及后置 `auth_sessions` 篡改与新增行检测、不可重放与 `Close()` 擦除）。
   - `go test -mod=readonly -count=1 ./internal/backup ./cmd/backup` 全部通过（`internal/backup` 3.390s，`cmd/backup` 0.125s）。
   - `go vet -mod=readonly -tags=integration ./...`、`gofmt -s -l` 与 `git diff --check` 全部通过；未运行 `-race`。
3. **Linux 隔离 PostgreSQL 16.15 + Redis + 真实归档/TLS 联合验收**：
   - 复用命令：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-old-refresh-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

   - 首次沙箱内调用因宿主机 Docker 命名管道访问受限未启动（`.toolchain/backup-restore-db-59849325b285/`）；随后使用授权宿主机 Docker 运行通过，完整证据目录 `.toolchain/backup-restore-db-16bc26d3ec70/`，`result.json` 为 `passed: true`、`passing_events: 127`、0 失败/跳过，4 个自有隔离容器 `cleanup` 均为 `true`。
   - `behavior_http` 实测：`good` 目标中 `users[0]` 在新登录创建前发起 1 次 `POST /api/v1/auth/refresh` 获得 `401` 并阻止重放，`users[1]` 在全部已知新会话确认 `Logout` 撤销后发起 1 次 `POST /api/v1/auth/refresh` 获得 `401`、阻止重放且 `auth_sessions` 快照零变更（`old_refresh_requests: 2`、`new_sessions: 2`、`active_after_dispatch: 0`）；`lost` / `redirect` / `wrong_pin` 保持原有失败边界且 `old_refresh_requests: 0`。
   - `bound_behavior_review` 保持 `bound-https-account-verification-v1`、`partial`、`10 pass / 2 unavailable`；旧 `isolated-http-fixture-v1` 与 CAS 登记（4→5、`validating`）不变。

| 证据项 | SHA-256 / 值 |
|---|---|
| Linux 测试二进制（`.toolchain/backup-old-refresh-linux.test`） | `01070d0576f5b830110383b703377c183685c76b0c1628555f985298d559df88` |
| 合成数据库归档 | `3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303` |
| PostgreSQL 16.15 镜像 | `sha256:f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657` |
| Redis 镜像 | `sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b` |

## 进入 H2 前的剩余边界

- `rejectOldRefresh` 当前为显式内部探测方法，尚未将（旧 access + 旧 refresh）双拒绝结果纳入 `confirmedBehaviorRun` 内存见证，因此 `bound-behavior-review.json` 中的 `old_token_http_rejection` 仍为 `unavailable`；上述见证绑定与报告提升将在 H2 中完成。
- 只读阅读行为回路（`reader_roundtrip`）、通用报告 CLI 诊断展示与 CAS 登记、受控恢复执行 CLI 仍按 H3–H5 顺序待办。
- 本轮未接触 `references\` 或任何生产数据库，未执行 git reset/clean/stash/checkout/commit。
