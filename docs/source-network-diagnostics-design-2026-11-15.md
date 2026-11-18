# 书源网络失败的隔离诊断

新增只读 `backend/cmd/sourceprobe`，不增加HTTP接口或公开错误码。只接受stdin中最多3个具名GET目标、64KiB总输入及每URL4096字节；单目标12秒总预算，响应上限1MiB，沿用Fetcher默认GET重试。它不执行书源JS、不连接数据库、不载入登录凭证或环境代理，不是实际书源/阅读验收替代品。

使用同一Fetcher及其SSRF/DNS/重定向/TLS校验，借助标准httptrace记录DNS、连接、TLS及首响应字节阶段，最多32个事件。输出仅含调用者指定的ASCII标签、时间、固定错误分类、已知HTTP状态及响应字节数，不输出URL/查询串/IP/证书细节/原始错误/头/Cookie/响应体。错误通过errors.Is/As分类，不解析错误字符串。

Fetcher耗尽5xx重试的最后状态改由类型化HTTPStatusError承载，仍匹配原有ErrUpstreamError及既有API映射。除此之外不改变重试次数、错误码或安全策略。未返回最终HTTP响应时状态未知，不推断为403或Cloudflare；只有既有ChallengeRequired才标记浏览器验证。

测试原始错误包含敏感地址但报告不泄漏、包装后的DNS/连接/TLS/超时和状态分类、事件上限并发安全、非法输入和SSRF保留；真实复测仅两个代表性失败源加example.com对照。Linux诊断容器非root、只读、无capabilities/端口/持久数据卷，只读挂载诊断可执行文件，256MiB/1CPU/64进程，工具结束自动清理。诊断未启用API的用户书源配额/凭证上下文，故只可评价网络层，不声称整个应用搜索通过。
