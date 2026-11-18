# 动作账本诊断及会话撤销复核验收

## 本轮实现

依据先行 [只读协议](backup-behavior-ledger-design-2026-11-16.md) 新增behavior_ledger.go及behavior_sessions.go。

DiagnoseBehaviorLedger两轮有界Get检查控制记录、意向及24个固定槽：验证原始父摘要、同operation/consent/index/user、时间顺序、合法kind、账号前缀和会话唯一性。完整logout响应仅标记responses_observed；response缺失但logout存在的合法失败窗口标记parent_unconfirmed，不从孤立记录提取可信session。畸形、不安全/不可读、读取中变化、矛盾和陈旧分别处理，失败不输出部分可信会话计数。

observeBehaviorSessions另需明确Plan、隔离DSN、独立source/target数据库pin，使用只读可重复读事务重验目标/角色/冻结账本，按完整response链中的session UUID核对owner和revoked。对授权user仅统计额外活跃数量，不反查/回填未知session ID，不调用登录/登出/Refresh，不修改数据库或账本。数据库前后重读完整链，变化或关闭失败不返回已核验结论。

输出是聚合观察，区分账本声明与数据库状态。已过期但revoked_at为空仍算活跃；零known不算成功；全部known已撤销但有未记账活跃增量也不算完整。所有dispatch/retry/deployment标记false、persistence_confirmation=not_inferred。它们不认证日志作者、不能证明记录中的会话确由本进程创建、不证明HTTP运行配置，不能变成behaviorGrant或报告pass。

此次没有新增诊断CLI/API或生产HTTP适配器。固定槽外文件未枚举，重复读取不是原子快照/ABA防护；只读结果描述本次数据库快照，不能保证之后无外部修改。

## 验证结果

- 新增4个顶层/22个子测试，共26个通过事件：正常声明与无授权、缺意向/dispatch/response/logout、合法孤立logout、错误父摘要/用户/索引/kind、未来/逆序时间、额外槽、重复/未知/过大JSON、过期、版本漂移、读中变化/权限/取消、重复会话、跳过账号和非法数据库入口。
- Windows Go1.26.11：backup 98个顶层/503个通过事件；cmd/backup 5个顶层/7个通过事件，无失败/跳过。日志 `.toolchain/backup-ledger-unit.jsonl`，不代表全仓库回归。
- Linux真实pg_dump/age恢复联合链 **98个通过事件**，无失败/跳过；上一轮86个事件及本轮新增12个断言（四组×账本/数据库观察，再加成功组4个反例）。
- go vet（integration标签）、gofmt、Python编译、git diff --check通过；65项契约检查、16个冻结文件摘要匹配，未改公开API/DDL/冻结归档协议。

| 派发夹具 | 账本状态 | 数据库观察 | known / revoked / 未记账活跃 |
|---|---|---|---|
| good | responses_observed | known_sessions_revoked | 2 / 2 / 0 |
| lost | outcome_unknown | unaccounted_active | 0 / 0 / 1 |
| intent_ack | outcome_unknown | no_known_sessions | 0 / 0 / 0 |
| response_ack | outcome_unknown | known_sessions_revoked | 1 / 1 / 0 |

额外真实数据库反例：过期但未撤销→known_session_active；记录指向的会话缺失→known_session_missing；会话改属另一用户→owner_mismatch；额外活跃会话→unaccounted_active。每次观察前后对比users/auth_sessions/schema_migrations行快照及控制区内容/权限，均未改变。反例由测试操作员显式施加并恢复，不属于观察函数写入；lost未知会话仍仅由夹具在断言后显式清理。

新模块没有扩大旧isolated-http-fixture-v1报告scope；既有完整报告/CAS链仍回归通过，operation仍validating。未知响应没有被改写成成功或恢复许可。

## 证据

运行 `.toolchain/backup-restore-db-30b048a1b6b4/result.json` passed=true，四个自有容器cleanup均true；ledger-evidence.json记录四组聚合观察。使用既有无宿主端口隔离编排，PostgreSQL network=none，Redis/测试共享其隔离网络，数据只在自有tmpfs。无业务DSN、真实用户或生产服务；不声称完成新增容器资源安全整改。

| 产物 | SHA256 |
|---|---|
| backup-ledger-linux.test | 2c47106e2dd60d9d0048452516efd8a896ebd1a10fcfc525963839121a899239 |
| 原合成database.dump | 3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303 |
| PostgreSQL16.15镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| Redis镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-ledger-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger
```

下一步先将纯记录诊断接到只读CLI，明确操作员核对流程，再接受限HTTP运输层和同次通用行为证据；不能只凭response.json或数据库当前revoked推断历史写确认。通用报告、受控恢复CLI、真实交接及发布门禁仍待完成。

本轮未部署、提交、推送、合并或重新生成Gemini审计包。分支codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。
