# 通用HTTPS账号行为部分审阅验收

## 本轮交付

依据先行 [部分审阅协议](backup-behavior-review-design-2026-11-17.md)，实现独立profile `bound-https-account-verification-v1`。报告固定partial：10项pass、old_token_http_rejection/reader_roundtrip两项unavailable；execution_authorized/ready_for_cutover/deployment_authorized均false。没有放宽旧isolated-http-fixture-v1或旧报告/CAS语义。

具体HTTPS运输层只有在登录响应经JWT+目标数据库核验、Logout经204+数据库撤销核验，并且同次意向/全部动作写入确认成功后，才将会话完成历史捕获到原live guard。见证绑定三控制记录、完整账本摘要、plan/consent、确切用户/会话、DB/运行修订/Redis身份和TLS证书摘要。类型不从JSON构造，不以磁盘完整记录或当前revoked补造历史确认。

派发开始时复制有界源/目标JWT材料；目标快照还须与运输层实际验签密钥一致。采集比较实际字节，不能仅保留修订UUID却更换JWT。副本只在私有内存，采集成功或guard.Close清除；失败派发清除未转移副本，不哈希秘密、不写报告。原输入未被清除；Go库/字符串副本不保证安全擦除。运输层Close清掉本地完成历史后，原guard仍保有独立见证副本，关闭guard则不能重建。

采集先要求调用者暂停本轮自有HTTP服务/连接池，随后重验目标身份、账本、实际会话撤销、对象/header及任务；会话增量仅允许本轮确切UUID/用户和有界创建窗口。纯只读采集失败可在原见证窗口内明确再观察，不自动循环、不重新登录。首次成功报告固定；保存单次锁存、create-only及回读，确认丢失保留、不覆盖、不改generation/phase。

## 验证结果

- 新增3个顶层/32个子测试，共35个通过事件：严格新profile/固定待验项、禁止全通过/授权true、unknown/duplicate/null/超限JSON、跨plan/consent/账本/控制/身份/证书/用户/会话/时间绑定，以及保存失败/确认丢失/回读失败/取消/重放保留。旧报告加载器拒绝新profile；见证JSON及秘密格式输出拒绝/脱敏。
- Windows Go1.26.11：backup 106个顶层/564个通过事件；cmd/backup 8个顶层/12个通过事件，无失败/跳过。日志 `.toolchain/backup-bound-review-unit.jsonl`；不表示全仓库回归。
- Linux真实pg_dump/age、TLS/API、账本/会话/身份和旧报告联合验收 **126个通过事件**，无失败/跳过。原118事件+8个新断言：JWT材料漂移拒绝、运行修订不符拒绝、正确部分报告采集、不可覆盖保存/回读、重复采集/保存拒绝，以及3个未确认HTTP场景拒绝。
- go vet（integration标签）、gofmt、Python编译、git diff --check通过；65项契约检查、16个冻结文件摘要匹配，无公开API/DDL/归档修改。

正常TLS夹具报告绑定2个账号/2个确切会话，十项已核验；两项待验保持原样。报告写入后原operation/attempt/publication字节与generation保持不变，phase仍validating。正常场景的写成功确认来自本次live调用，文件读取不能重建它；丢响应、重定向、错误证书场景没有见证，均不能生成报告。运行配置仍是显式装配核对，不升级为全部运行实例配置实测。

## 证据

首轮 `.toolchain/backup-restore-db-eed6aaebd81c/` 通过125事件；随后补实际JWT材料与修订之间的一致性及副本清除断言，重建最终二进制重跑全部链。

最终 `.toolchain/backup-restore-db-7e1f4a153ee4/result.json` passed=true，四个自有容器cleanup均true。bound-behavior-review.json为独立部分报告，scope=localfs-bound-https-account-review；旧behavior-review.json仍属isolated-http-fixture-v1，不是同一个profile，不能拼接其pass填补新报告待验项。

| 产物 | SHA256 |
|---|---|
| backup-bound-review-linux.test | 3839d74d8b40112bcde25c65ad4df4bf94e799fe53339266982377d6926ec3bb |
| 原合成database.dump | 3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303 |
| PostgreSQL16.15镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| Redis镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-bound-review-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

沿用自有无宿主端口隔离编排，PostgreSQL network=none，测试和两个独立Redis共享其隔离网络，数据仅tmpfs；TLS只在隔离网络内本机监听。使用合成账号/书籍，未访问业务库或开放局域网。没有新增容器资源安全整改声明，也未运行race检测。

## 后续边界

下一步补同次旧令牌HTTP拒绝及明确授权的只读阅读行为证据，再评审报告profile的演进；登录/登出不是完整阅读验收，旧refresh接口可能撤销用户其他会话，不能直接当作无副作用探测。新报告尚无公开展示/执行CLI、旧CAS登记或生产交接入口，不能推进ready_for_cutover/completed。多次读取也不是原子快照或对外部管理员写者的全局封锁。

本轮未部署、提交、推送、合并或重新生成Gemini审计包。分支codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；真机/ARM/NAS等待验门禁保持原状。
