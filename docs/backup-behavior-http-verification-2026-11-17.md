# 受限HTTPS运输层验收

## 交付范围

依据先行 [HTTPS契约](backup-behavior-http-design-2026-11-17.md)，新增behavior_http.go：显式本机literal HTTPS端点、固定叶证书DER/SHA256、IP SAN/时效核验、固定拨号地址，禁代理/cookies/压缩/HTTP2/keep-alive、禁重定向及POST重放。每授权用户最多一次网络登录尝试，已知会话Logout调用也提前锁存。旧源access/refresh和源JWT密钥不进入此运输层。

登录200响应严格核对冻结JSON字段及nullable User、目标JWT签名/subject/version/time；再用目标只读事务核对数据库/角色pin、账本、refresh对应的精确会话/用户/版本/时效，才返回已知UUID。Logout仅使用此实例记住的同用户会话，要求204后再次查库确认revoked，不调用Refresh或撤销未知/全部用户会话。Close仅清除本地材料，不自动登出；不承诺Go字符串/网络库副本安全擦除。

TLS使用InsecureSkipVerify配合自定义完整证书pin、IP及日期检查，替代公共CA链；不是无校验TLS。证书与目标配置的对应关系仍依赖可信装配者，不能由任意健康响应自证；会话核对并不证明服务的全部业务依赖正确。HTTP服务自身可持有同库连接，因此会话解析仅放宽原accountObservation的“无其他连接”条件，保留所有pin/角色/只读/账本要求；原账号/会话观察仍默认排他。

模块仅内部显式装配，不启动生产服务、不新增配置/执行CLI/API。临时TLS服务只在本轮隔离测试中建立。它不改变书源SSRF策略、合成profile或派发/报告/切换授权。

## 验证结果

- 新增5个顶层/21个子测试，共26个通过事件：非法URL/绑定、证书pin与拨号限制、秘密格式/JSON拒绝与Close、nullable/重复/未知/无效UTF8/过大响应、JWT签名/版本/过期、错误状态/网络/压缩/关闭失败不重放，以及请求体独立缓冲和清理。
- Windows Go1.26.11：backup 103个顶层/529个通过事件；cmd/backup 8个顶层/12个通过事件，无失败/跳过。日志 `.toolchain/backup-http-transport-unit.jsonl`，不表示全仓库回归。纯单测使用假RoundTripper/证书检查；真实TLS网络行为由下述Linux链验证。
- Linux amd64真实pg_dump/age恢复联合验收 **118个通过事件**，无失败/跳过；包含前一轮98事件及新增4组独立恢复目标×5事件=20事件。实际API Handler经TLS登录/登出，查库确认会话增量和撤销；没有业务账号或生产目标。
- go vet（integration标签）、gofmt、Python编译、git diff --check通过；65项契约检查、16个冻结文件摘要匹配。未修改公开API、DDL、账本或备份格式。

| HTTPS场景 | 登录HTTP请求 | 登出HTTP请求 | 新会话 / 派发后活跃 | 重定向接收端请求 |
|---|---:|---:|---|---:|
| good | 2 | 2 | 2 / 0 | 0 |
| lost | 1 | 0 | 1 / 1 | 0 |
| redirect | 1 | 0 | 0 / 0 | 0 |
| wrong_pin | 0 | 0 | 0 / 0 | 0 |

lost在API登录提交后关闭连接，客户端未重发、不猜测会话；断言后仅测试操作员按夹具查询到的确切UUID清理。redirect使用另一个TLS接收端计数，未跟随；wrong_pin握手失败且目标零登录POST。还注入假代理环境并检查运输层Proxy=nil，测试无cookie/幂等重试头或多余端点。已知正常会话不仅收到204，还经固定目标库核验revoked。目标数据集只出现授权用户增量，旧会话不删除。

首次联合运行 `.toolchain/backup-restore-db-db040aad8fa1/` 已通过118事件；复核后补请求体独立缓冲（避免Do返回后的异步读取/清理共享字节），重建并再次跑完整链。未执行race检测，不将同步代码检查称作race验收。

## 最终证据

`.toolchain/backup-restore-db-13eb7ee143a7/result.json` 为passed=true，四个自有容器cleanup均true；https-evidence.json记录四组聚合数据，scope=isolated-https-dispatch、deployment_authorized=false。既有完整报告仍仅isolated-http-fixture-v1，CAS仍4→5、operation phase=validating，不因新HTTP观察而自动放行。

| 产物 | SHA256 |
|---|---|
| backup-http-transport-linux.test | 8742f18409d05331a685987952985608bb1af4a4455893f3874eb64565284cbd |
| 原合成database.dump | 3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303 |
| PostgreSQL16.15镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| Redis镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-http-transport-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http
```

沿用自有无宿主端口隔离编排，PostgreSQL network=none，测试/独立Redis共享其隔离网络，数据仅tmpfs；TLS监听只在该隔离网络内的本机地址。没有局域网开放、部署或业务库访问，不声称本轮完成新增容器资源安全整改。

## 下一步

确定通用行为证据profile与最小检查集合，将同次HTTPS派发、账本、数据库会话观察及运行身份绑定；登录/登出成功不等于完整阅读行为验收。不得直接放宽旧合成profile或把当前revoked反推历史写确认。公开受控恢复入口、真实用户授权装配、升级交接和发布门禁仍待完成。

本轮未提交、推送、合并或重新生成Gemini审计包。分支codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；iPhone/ARM/NAS待验项保持原状。
