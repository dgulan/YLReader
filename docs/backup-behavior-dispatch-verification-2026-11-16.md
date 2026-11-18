# 一次性行为派发与会话响应记账验收

## 已交付

依据先行 [派发与记账契约](backup-behavior-dispatch-design-2026-11-16.md)，新增behavior_dispatch.go：restoreGuard内部单次尝试锁存，先账号核验，再持有guard及租约核对版本/物理绑定，确认唯一意向及每账号dispatch记录持久化后才调用一次Login。确认丢失、响应未知、取消或绑定变化停止后续账号；没有自动重试或Refresh调用。

有效同用户session UUID单独记response，按已知会话最多尝试一次Logout，再记logout响应。重复/空/异用户会话不授予Logout。父context取消时，对已经确认的同用户会话使用独立2秒上下文做意向已包含的受限登出及记账，不扩大到该用户全部会话。response记录失败也允许在内存中已知会话上做这一次清理，持久结果仍按未知保留。

动作记录create-only、严格JSON、父摘要串联，仅含UUID/非秘密绑定/响应类别；不含用户名、口令、token或文件路径。运输层收到的副本清除了旧access/refresh，原预检输入未修改。结果摘要禁止JSON序列化，不是报告证明或重启许可。

本轮仅内部派发内核及测试用AuthService适配器，没有生产HTTP适配器、外部URL/插件回调注册、执行CLI/API。Binding是可信内部适配器的声明，不是远端身份探测；未来HTTP运行器仍须每次连接核对目标pin/角色、禁止重定向/自动重试。观察及guard检查不是对外部管理员写者的原子封锁；派发proof仅作短时内部输入，不作持久执行授权。

## 验证

- 新增5个顶层/17个子测试，共22个通过事件：成功及原始凭据不泄漏、唯一意向重放、8路并发仅一批登录、意向/派发/response/logout确认丢失、response写失败、登录/登出响应未知、取消后已知会话清理、错误/空/重复session、绑定/guard/记录/新鲜度/取消前置条件拒绝。
- Go1.26.11 Windows：backup 94个顶层/477个通过事件；cmd/backup 5个顶层/7个通过事件，无失败/跳过。日志 `.toolchain/backup-dispatch-unit.jsonl`。不是全仓库测试声明。
- Linux真实pg_dump/age恢复联合链86个通过事件，无失败/跳过。包含上一轮66个事件及新增4个独立恢复目标×（恢复子测试+4个派发断言）=20个事件。
- go vet（integration标签）、gofmt、Python编译、git diff --check通过；65项契约检查、16个冻结文件摘要匹配，未改公开API/DDL/冻结备份格式。

| 独立合成目标 | Login调用 | Logout调用 | 核心执行后新增/活跃会话 | 结论 |
|---|---:|---:|---|---|
| good | 2 | 2 | 2 / 0 | 两用户各一次，已知会话实际撤销 |
| lost | 1 | 0 | 1 / 1 | 登录已提交但响应丢失，停止，不猜测session或重试 |
| intent_ack | 0 | 0 | 0 / 0 | 意向写成功但确认丢失，未派发 |
| response_ack | 1 | 1 | 1 / 0 | response登记确认丢失，已知session清理一次，第二账号未登录 |

测试适配器直接调用真实AuthService，按refresh摘要查询确切会话UUID且Logout后查库确认revoked。lost目标中的未知活跃会话先留存并断言，之后只由测试操作员按夹具内已知token清理；不是派发器自动恢复。所有模式都验证再次调用同guard零新增登录；原始2条旧会话不删除，无跨用户增量。Login会按现有契约更新所选用户last_login_at/updated_at，这是已授权的登录副作用，不将整个数据库描述为未变化。

意向诊断在全部模式下仍outcome_unknown/not_inferred、禁止dispatch/retry，不能从完整文件推断历史确认。response/logout记录名称只代表运输层响应；本轮测试适配器另行查库验证，通用账本尚不能单独作为真实会话撤销证明。新派发目标不生成通用十项pass报告；此前isolated-http-fixture-v1报告及CAS回归仍独立通过，phase仍validating。

## 证据与复跑

最终 `.toolchain/backup-restore-db-8152a53bd117/result.json` passed=true；四个自有容器cleanup均true。dispatch-evidence.json保存四种合成结果，scope=isolated-service-dispatch、deployment_authorized=false。

| 产物 | SHA256 |
|---|---|
| backup-dispatch-linux.test | abb8a3e3f06db0c90d0e993ea3bed60b32a77f9d2084d5b0c010d67b0dd0ffd3 |
| 原合成database.dump | 3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303 |
| PostgreSQL16.15镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| Redis镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-dispatch-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch
```

沿用既有隔离恢复编排：无宿主端口，PostgreSQL network=none，测试与两个独立Redis共享其隔离网络；数据库/Redis仅自有tmpfs。只使用合成数据和账号，未接触业务库。本轮不声称完成生产运输层或新增容器资源安全整改。

## 下一步

补动作账本链的只读诊断及已知会话数据库复核，区分响应声明/真实撤销/未知父记录，再接受限HTTP运行器与通用行为报告。不能把既有合成profile直接放宽，也不能把新账本当作跨重启恢复许可。受控恢复CLI、真实用户交接及发布门禁继续待办。

本轮未部署、提交、推送、合并或重新生成Gemini审计包。分支codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。
