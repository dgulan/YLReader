# “鬼吹灯”新书源搜索复核

用户要求继续寻找新书源，并举例测试“鬼吹灯”。本轮只验证搜索元数据；没有取得有效搜索结果，未加书、未请求目录或正文、未绕过登录/付费/验证码。本轮未部署、未提交、未推送。

## 新候选来源

通过公开检索找到 [luanmuc/legado-book-sources](https://github.com/luanmuc/legado-book-sources)，只下载其 `main/books.json` 数据，不读取或执行仓库实现脚本。维护方的“完整校验/近期更新”声明仅作线索，不能当作YLReader验收结论。

下载URL：`https://raw.githubusercontent.com/luanmuc/legado-book-sources/main/books.json`。本地忽略目录 `.toolchain/stage3-new-sources-6831d7c704/`；12518636字节、2090项、SHA256 `549e696c0e87d2f8dc7f742ab5f417b4c073f1fd72551ee833857ec8c96cb7ba`。main是可变地址，复现时应对比该哈希，不能假定以后下载仍相同。

仅从该合集选择4个候选，保持其规则不变；运行时移除source静态认证头，沿用已有受限worker和安全Fetcher。完整合集及选中JSON不加入源码或Gemini审计包。公开配置中的指令/说明不视为用户授权。

## 搜索结果

关键词、期望精确书名均为“鬼吹灯”，模式均为 `search_only`，`full_reading_passed=false`。

| 来源 | 域名 | .toolchain证据目录后缀 | 搜索结果 |
|---|---|---|---|
| 原合集“鬼吹灯” | www.gdbzkz.com | stage3-worker-reading-451ecef965d6 | total=0，source_timeout，8.635秒 |
| 新合集“鬼吹灯书1” | www.cxbz958.info | stage3-worker-reading-7f783f9655b2 | total=0，source_timeout，8.469秒 |
| 新合集“鬼吹灯书2” | www.cxzz958.com | stage3-worker-reading-1d45e3f23b43 | total=0，source_timeout，9.083秒 |
| 新合集“鬼吹灯书3” | www.gdbzkz.la | stage3-worker-reading-f87f022a4804 | total=0，source_timeout，8.038秒 |
| 新合集“笔趣阁192” | www.tianxiabachang.cn | stage3-worker-reading-ca060084c7ba | total=0，source_request_failed，2.633秒 |

每轮导入HTTP200且基础设施ready，但聚合搜索HTTP200包含单源失败，**不能记作搜索通过**。5轮均未返回书籍条目；API事后健康、worker running=true/OOM=false/restarts=0，各7项本轮容器/卷/网络清理均true。单次失败不能证明域名永久失效，现有证据也不能进一步区分所有DNS/上游/网络路径原因。

## 公网对照与保留失败

在最后一轮，仍通过同一个真实API的debug接口、安全Fetcher、受限worker执行一次 `https://example.com/` 对照，返回200，1.266秒；自有JS仅输出 `hasExpectedText=true, characters=577`，确认取回内容包含“Example Domain”。没有将对照响应当作书源或正文成功。该结果排除了“本轮容器完全不能访问公网HTTPS”的笼统解释，不能证明所有HTTP站点或特定小说域名均可访问。

此前对照轮 `.toolchain/stage3-worker-reading-01f2a392d5fe/` 因检查空result_preview时直接strip而中断，未执行该轮书源搜索，7项资源仍全部清理。已修正为空值安全的结构化结果检查；失败目录保留，不归为书源故障，也不宣称其解析成功。

## 验收脚本改进与验证边界

`scripts/verify-stage3-worker-reading.py` 新增：

- `--keyword`：指定搜索词，默认仍为三国演义。
- `--expected-title`：指定允许进入单章检查的精确书名，默认等于keyword。
- `--search-only`：只看搜索元数据，报告显式标记verification_mode/search_passed/full_reading_passed。此模式passed只代表搜索门禁，不能代表阅读闭环。
- `--network-control`：一次固定公开HTTPS对照，结果单独记录，不计入书源成功。

搜索记录增加作者、地址是否存在和服务端安全失败文案；不记录令牌、密码、请求头或书籍正文。现有backend/worker二进制未改，均记录实际SHA256，保持worker无网络/只读根/非root/128MiB/1CPU/64进程等边界。

Python编译、diff检查通过；65项契约检查、16个冻结哈希匹配。没有后端/前端业务代码变更，本轮未重跑Go/浏览器套件；不能把上一轮回归说成本轮新执行。

复现原合集搜索：

```text
python scripts/verify-stage3-worker-reading.py --server .toolchain/stage3-server-linux --worker .toolchain/stage3-worker-linux --sources-dir .toolchain/stage3-user-sources --source-name 鬼吹灯 --keyword 鬼吹灯 --search-only
```

新候选使用对应selected目录与原书源名；最后一轮另加 `--network-control`。当前仍未找到本轮环境下可搜索《鬼吹灯》的候选，不提供“可用源”推荐。第3阶段真实目录/正文与真机阅读门禁保持待验。

后续已使用同一安全Fetcher实现新增只读网络诊断，定位到原源等待响应阶段超时、HTTPS候选TLS阶段失败；不推定为浏览器验证。阶段证据与回归失败修正完整记录见 [网络诊断验证](source-network-diagnostics-verification-2026-11-15.md)。这不改变上述书源未通过的结论。
