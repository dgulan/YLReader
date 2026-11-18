# 书源网络诊断验证

本轮未部署、未提交、未推送，不关闭TLS证书校验、不修改SSRF或凭证策略。新增sourceprobe CLI及隔离运行脚本，设计见 [网络诊断设计](source-network-diagnostics-design-2026-11-15.md)。它不执行书源JS或解析书籍，不是阅读验收的替代品。

## 网络层实测

证据 `.toolchain/source-network-dff33e8ebc88/`。当前源码静态Linux诊断程序SHA256为 `ffc9a0894b70cba31bb7d59091cdd1ded90031243ff0c7a76ea61a1a7f243855`。容器非root/只读、256MiB/1CPU/64进程、cap-drop ALL/no-new-privileges，无发布端口或持久数据卷；运行结束cleanup=true。

| 标签/对应目标 | 结果 | 阶段证据 |
|---|---|---|
| control / example.com | ok，HTTP200，577字节，1.213秒 | DNS、连接、TLS、首响应字节成功 |
| ghost_old / 原鬼吹灯搜索地址 | deadline，8.481秒，最终HTTP状态未知 | DNS完成且随后连接成功，418ms曾收到首响应字节；随后另一次DNS/连接完成，直至失败未记录到该连接首响应字节 |
| ghost_https / tianxiabachang.cn搜索地址 | tls_error，2.180秒，最终HTTP状态未知 | 三次尝试均DNS/连接完成，tls_done=false，未到正文解析 |

deadline分类表示错误链匹配context.DeadlineExceeded，**不表示12秒总预算一定耗尽**；net/http内部等待超时也可匹配。连接事件可能包含DNS解析器内部连接，判断目标连接应结合dns_done之后的事件。最多保留32个事件，不将缺失事件当作明确HTTP状态。

本次未出现ChallengeRequired，不应推定为Cloudflare过盾问题。tls_error目前只区分到TLS阶段，尚未细分所有证书与协议原因；不以放宽证书验证作为修复。网络probe没有API用户配额、加密头或书源上下文，所以仅能说明本轮网络层现象，不能推定所有应用失败都同因，更不能宣称域名永久失效或书源已可用。

报告passed=true表示工具正确执行且清理成功，purpose明确为network_only_not_source_reading；两条失败源并未通过可用性验收。没有新增有效搜索结果、目录或正文证据。

## 代码与回归

- Fetcher最终5xx采用HTTPStatusError保存状态，仍通过errors.Is匹配ErrUpstreamError，原API错误映射保持不变。新增测试验证503仍尝试3次，类型化状态可取且不泄漏URL/响应体。
- sourceprobe通过errors.Is/As分类DNS、连接、TLS、超时、策略及浏览器验证；只输出固定分类、时间/状态/字节数和调用者标签，不输出原始错误、URL/IP、头或正文。输入最多3个目标/64KiB，逐个GET最多12秒/1MiB；非法输入及私网URL拒绝测试通过。
- Windows普通测试：cmd/sourceprobe、fetcher、service、api四包通过。
- 首轮Linux race `.toolchain/source-probe-race-20261115.jsonl` 为31 pass/1 fail：旧TestSourceMemoryStateBounds用1秒租约填4096条，race耗时1.09秒，合法自然过期造成容量断言误判。
- 第二轮 `.toolchain/source-probe-race-final-20261115.jsonl` 同为31 pass/1 fail：本轮曾把测试租约设成1小时，超出生产既有1分钟参数上限，首条就被拒绝；这是夹具修正中的错误，不是生产缺陷。
- 最终改用合法1分钟租约，仅在测试夹具中固定源状态到期时间，然后显式让一条到期。既验证4096上限，又验证仅替换该过期条目；没有改变生产上限或回收代码。
- 最终Linux race证据 `.toolchain/source-probe-race-verified-20261115.jsonl`：cmd/sourceprobe与fetcher共32项顶层测试通过，0 fail、0 skip。失败轮次保留，不重复执行至绿而忽略原因。
- 65项契约检查、16个冻结哈希匹配，gofmt/diff检查与Python编译通过。本轮没有全量数据库或浏览器回归声明。

复现：构建当前源码的linux/amd64、CGO_ENABLED=0 `./cmd/sourceprobe`，准备只含公开GET目标和ASCII标签的本地JSON，再运行：

```text
python scripts/verify-source-network.py --binary .toolchain/source-network-linux --input .toolchain/source-network-input.json
```

原始输入仅保存在忽略目录，不上传审计包；工具实现、测试、设计和本报告加入审计包。第3阶段真实阅读门禁仍未完成。
