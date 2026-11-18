# 搜索请求选项的首批兼容范围

不改公开API、DDL或冻结哈希，保留独立worker显式启用及全部Fetcher安全过滤。本设计只扩展内部searchUrl解释，不能作为浏览器交互/代理/登录能力的授权。

支持普通URL，以及Legado的 `URL,{...}` 后缀。首批选项仅method、body、headers、charset：GET/POST，UTF-8或未指定编码；POST只发送显式UTF-8表单字符串，默认Content-Type为application/x-www-form-urlencoded。key以QueryEscape回填、page为十进制；POST不把默认searchKey追加到URL，GET保留既有默认查询参数行为。请求体最多64KiB、完整规则最多64KiB。

headers仅允许User-Agent、Accept、Content-Type；忽略空值，拒绝非空Cookie/Authorization/Host/代理等其他头、重复字段/忽略大小写重复头、CR/LF及不符合上述表单Content-Type的POST。书源已有的加密头及其来源绑定策略不变；本选项不新增凭证通道。未知选项、GBK请求编码、动态JS URL和浏览器交互明确返回既有unsupported_rule，且在任何网络请求之前失败，不擅自降级为GET。

POST只能调用已有Fetcher.Fetch，继承SSRF/DNS/重定向/响应预算及“不自动重试POST”；不具备该窄能力的测试/替代Fetcher返回unsupported_rule。普通GET继续使用Fetcher.Get。目录/正文接口不在本轮请求选项扩展范围，不能据搜索通过宣称整源兼容。

用原创JSON/表单和窄Fetcher桩验证method、body、URL回填及拒绝边界；以既有Fetcher的POST重试测试复核安全语义。真实书源只作补充证据，上游失败不靠放宽安全边界解决。
