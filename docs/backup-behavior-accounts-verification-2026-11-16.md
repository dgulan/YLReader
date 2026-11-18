# live guard 通用恢复账号核验验收

## 本轮交付

依据先行 [账号核验契约](backup-behavior-accounts-design-2026-11-16.md)，实现 behavior_accounts.go。新路径接收独立的preparedBehaviorInput，不依赖合成profile或固定用户名；复用已有数据库/运行修订/Redis/数据探测，再持有同次live guard核验原计划、控制版本及物理位置，在独立READ ONLY/REPEATABLE READ事务内核对数据库pin/角色/账本、账号口令、旧JWT及refresh归属/撤销/有效期。结束重验时效、guard和控制记录。

复用service.VerifyPassword已有参数上限，按账号顺序校验；旧JWT校验新增可注入时钟的内部helper，原合成路径保留time.Now包装。所有外部错误固定，不记录用户名、hash、密码、令牌、DSN或秘密摘要。refresh SHA256仅为既有查库协议在内存中计算，不保存到证明。证明禁止JSON序列化，fmt输出脱敏。

这是只读账号观察，不是behaviorGrant，不是登录派发许可，也没有公开CLI/API。运行修订核验仍只证明显式内存装配，不证明某个运行服务配置；事务是读取快照，不能保证外部写者随后不改账号。后续执行器必须再次核验并使用持久意向和一次性能力。ready_for_cutover/completed及通用真实用户报告仍未放行。

## 验证结果

- Windows Go1.26.11：backup 89个顶层/455个通过事件；cmd/backup 5个顶层/7个通过事件，无失败或跳过。日志 `.toolchain/backup-accounts-unit.jsonl`。新增1个顶层/6个子测试，覆盖关闭输入、相同/短JWT密钥、错账号/空UUID、非法数据库pin和nil guard等连接前拒绝；既有JWT时效及合成profile测试同时回归。
- go vet（integration标签）、gofmt、Python编译、git diff --check通过；65项契约检查、16个冻结文件摘要匹配，无公开API/DDL/冻结归档格式改动。
- Linux真实pg_dump/age恢复、对象/header、HTTP、部分/完整报告及CAS登记联合链：**66个通过事件，无失败/跳过**，其中新增14项通用账号检查。旧合成报告仍仅isolated-http-fixture-v1；CAS仍4→5、phase=validating。

新增14项：正确账号、错误口令、错误username、禁用账号、过期access、错误access签名、错误token_version、跨用户refresh、未撤销refresh、相同JWT密钥、错误数据库pin、运行修订不符、owner不符、已关闭guard。部分反例被前置数据/身份检查先行拒绝，不能声称每个反例均抵达账号SQL。

每次核验调用前后对比users/auth_sessions/schema_migrations行快照，以及控制区内容/权限。数据库不变性证据限这些表；不是完整数据库物理字节快照。禁用/撤销反例由隔离夹具明确改变并恢复业务字段，users触发器会更新时间，不把夹具变更算成核验写入。新账号路径未派发登录；随后原HTTP回归仍按已有合成profile登录/读写/登出并核验完整报告，不代表通用用户运行器已实现。

## 运行证据与修正

首轮 `.toolchain/backup-restore-db-99930206a00e/` 的14项账号场景通过，但跨全部反例的最终行快照因夹具触发users.updated_at而失败。随后将不变性比较移到每次明确夹具变更之后、核验调用前后，保留反例及断言，没有忽略timestamp列；补充连接角色与guard配置一致性检查，重建最终二进制并重跑整个链。

最终 `.toolchain/backup-restore-db-faa29c47c258/result.json` 为passed=true，四个自有容器cleanup均true。accounts-evidence.json scope=guarded-synthetic-accounts、checks=14、login_dispatched=false、execution_authorized=false。测试目标是合成账号，不是操作员真实账号。

| 产物 | SHA256 |
|---|---|
| backup-accounts-linux.test | 1d1cee6cf243764d7e556538eba71a37fb6177e031cb94e03cd276292084f241 |
| 原始合成database.dump | 3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303 |
| PostgreSQL16.15镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| Redis运行镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

复跑命令（先用仓库Go工具链构建Linux integration测试二进制）：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-accounts-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts
```

脚本只使用本轮自有network-none PostgreSQL与共享其隔离网络的独立Redis/测试容器，无宿主端口、业务DSN或真实凭据；PostgreSQL/Redis数据仅tmpfs。使用的是既有恢复验收编排，本轮没有声称额外完成容器资源安全整改。首轮失败资源也全部清理。

## 下一步

将账号观察、同次live guard、唯一动作意向接到一次性派发能力，并实现已知会话UUID的有界结果登记/登出及响应丢失诊断。不能把新观察转换成已有合成profile报告的pass，不能从意向文件恢复派发许可。通用真实用户报告及受控恢复入口继续待办。

本轮未部署、提交、推送、合并或接触业务库，未重新生成Gemini审计包。分支codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。
