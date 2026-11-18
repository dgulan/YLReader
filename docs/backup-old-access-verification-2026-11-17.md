# 旧 access token HTTPS 拒绝探测验收

## 本轮范围

依据 backup-old-access-design-2026-11-17.md，实现具体受限 HTTPS 运输层的显式内部 rejectOldAccess 方法。白名单用户、源签名/版本/时效及不同目标密钥通过本地核验后，只 GET 固定 /api/v1/auth/me；响应后再次核验源令牌时效。每用户发出前锁存，错误后不重发；复用固定证书、禁代理/重定向/连接复用客户端。只确认有界、未压缩、符合 Error 结构的 401 unauthorized；关闭响应失败也拒绝确认。

不传 refresh、密码，不调用 refresh/logout/login，不自动加入派发，不写账本、不升级运行见证。Close 清除尝试历史。当前仅有内部能力和隔离合成夹具调用，没有公开执行入口、生产生命周期或真实账号授权流程。

## 验证

- 新增一个顶层测试、18 个子场景，共 19 个通过事件：成功、200/403/重定向、网络/关闭失败、压缩/超大/重复字段/错误结构、取消、错误源密钥、目标签发、过期、用户/版本不符和已关闭；核对请求路径/方法/无 body/无 Cookie、每用户最多一次发送。
- 本机 go test -json -count=1 ./internal/backup ./cmd/backup 全通过：backup 107 个顶层、CLI 8 个顶层，总计 115；日志 .toolchain/backup-old-access-unit.jsonl。
- go vet -tags=integration ./internal/backup ./cmd/backup、Linux 静态测试二进制编译通过；未运行 race。
- 完整隔离 PostgreSQL16.15、Redis、真实归档/TLS 联合验收 126 个通过事件。good 目标两位用户各一次旧 access 请求，随后各一次登录/登出；旧会话两条加本轮两条、活跃会话为零，未增加其他会话。lost/redirect/wrong_pin 保持既有失败边界，未额外调用旧 access 探测。
- 通用部分报告仍为 bound-https-account-verification-v1、partial、10 pass/2 unavailable；旧 access 结果不拼接到报告。旧 isolated-http-fixture-v1 及其 CAS 仍是独立合成目标证据，不代表通用生产闭环。
- 契约检查 65 项、冻结摘要 16 份一致；Python 编译与 git diff --check 通过。

最终证据目录 .toolchain/backup-restore-db-813f70d8f79b/，result.json passed=true，四个自有临时容器 cleanup 均 true。首次非提权调用因 Docker 管道无权限而未开始验收，记录于 .toolchain/backup-restore-db-132b127696e6/；随后使用授权的本机 Docker 隔离测试完成，不访问业务数据库或外部书源。

| 项目 | SHA-256 |
|---|---|
| Linux 测试二进制 | 69121dfb67817e9af1a7d9443622045ec55cc9af0d50e27a9c826e8b689dbd98 |
| 合成数据库归档 | 3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303 |
| PostgreSQL 镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| Redis 镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

复现命令：

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/backup-old-access-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

## 剩余边界

401 只是固定端点对所给旧 access 的拒绝观察，不单独证明拒绝原因、服务运行配置或旧 refresh 安全。本方法没有持久化结果、同次 guard 见证绑定或恢复授权效力。old_token_http_rejection 仍 unavailable。下一步先设计旧 refresh 的显式授权和副作用隔离（其复用检测可能撤销同用户所有活跃会话），以及同次旧令牌观察结果的绑定，再实现只读阅读行为与报告 profile 演进。

未创建 YLReader-0，未部署、提交、推送、合并、接触业务库或重新生成 Gemini 上传包。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；真机/ARM/NAS 等既有门禁不变。
