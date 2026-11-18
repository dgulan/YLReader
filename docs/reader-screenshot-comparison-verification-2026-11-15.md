# 用户reader截图对照与正文兼容修复

本轮未部署、未提交、未推送。用户补充的截图来自changshengyu/reader:latest，显示鬼吹灯、笔趣网、抖音小说、必去书库、无极小说等换源结果。书籍已由用户确认：**《顶级气运，悄悄修炼千年》，作者任我笑**。设计见 [截图对照设计](reader-screenshot-comparison-design-2026-11-15.md)。

## 对照条件及历史结论边界

用户说明reader运行于172.16.1.16、未使用代理；开发机为172.16.1.18，两机DNS均为172.16.1.14。.18尚未部署YLReader，没有固定服务地址。本轮通过.NET网卡枚举确认当前开发机包含.18地址；实际测试均是其Docker Desktop中的独占临时Linux API/PG/Redis/无网络worker。没有访问或修改.16运行实例，也没有读取旧项目代码。

同一DNS和局域网不等于两台宿主机/容器的路由、默认UA、TLS信任及超时完全一致。用户截图是有效的原软件使用对照，不应把YLReader临时容器失败解释为书源普遍失效。此前“鬼吹灯”同时是源名和测试书名；以《鬼吹灯》为关键词的失败不能替代本轮这本书的验证。

重新获取用户指定两份合集，SHA与此前完全一致：

- `71e56d4f.json`：22项，`24fe11c400ddc8754703ff0c4d8a249baf024da3a15563de805cc134140959a0`。
- `4dc410d1.json`：128项，`05da3da45910c8342d4935044c9af6298be0fbcee62af910b6aea2550fee44de`。

图中五个源均来自第二份。它们没有预置非空source Cookie/认证头，因而本轮清空source.header并未移除这些源的必要预置凭据。原始配置保持不变，保存在忽略目录 `.toolchain/reader-comparison-0d21c086f2/`，provenance记录单源哈希。

## 相同书名的五源搜索

| 源 | .toolchain证据目录 | YLReader本轮结果 |
|---|---|---|
| 鬼吹灯 | stage3-worker-reading-9d575ea97826 | source_timeout，8.644秒，0条 |
| 笔趣网 | stage3-worker-reading-be617e269cb0 | source_request_failed，1.318秒，0条 |
| 抖音小说 | stage3-worker-reading-3e68ec42547d | **9条有效结果、无失败码**，3.340秒；精确匹配用户书名及作者，地址存在 |
| 必去书库 | stage3-worker-reading-0a70a40a2b80 | source_timeout，9.800秒，0条 |
| 无极小说 | stage3-worker-reading-b7d9a9c292b0 | source_timeout，10.050秒，0条 |

5轮都是search_only，未请求正文；各7项临时容器/卷/网络清理全部true。仍失败的四源保留为本环境结果，尚未对其与reader的请求/网络差异作最终归因。抖音的UTF-8表单POST使用此前已补齐的安全Fetcher路径，不调用浏览器过盾。

## 确认并修复的YLReader缺陷

首次抖音单章链路 `.toolchain/stage3-worker-reading-fb2dab8863a7/`：搜索9条、加书201、目录200/112项，但正文18ms即422/rule_execution_failed。定位到parseContentRules：规范形式 `content.content` 和旧式字符串 `ruleContent` 可读取，却遗漏真实Legado对象 `ruleContent.content`，使本地判断正文规则为空，尚未抓取正文就返回422。

修复仅增加该字段回退，保持规范组优先、旧字符串兼容、分页/替换规则及非法类型处理不变，不改变公开API、DDL、TLS或SSRF。原创JSON/HTML单测先失败后通过，验证对象规则能实际进入Fetcher并提取原创段落，以及规范组优先和null/数字规则仍为空。

修复后 `.toolchain/stage3-worker-reading-b2f0e7243a1c/` 已返回单章HTTP200/2631字符。其旧字段full_reading_passed当时表示有限API链路，命名过宽，不能解释为目录或整书通过；原证据不修改，后续脚本已改为显式single_chapter_passed。

最终使用**书名和作者双重精确匹配**的 `.toolchain/stage3-worker-reading-5d263a718062/`：

| 项目 | 结果 |
|---|---|
| 搜索 | 9条有效结果，无失败码；匹配任我笑 |
| 加书 | HTTP201 |
| 目录 | HTTP200，112项 |
| 第一条目录元数据 | index=0，第1192章 终元至高【二合一】 |
| 最后一条目录元数据 | index=111，第100章 来自周凡的挑战 |
| 单章正文 | HTTP200，2631字符，1.100秒 |
| 正文摘要 | SHA256 `422b9a76e4b26856a1a756d66b5e154d6d5b68823bc2ae2007299b89b29fae93`；不保留/输出正文 |
| 门禁 | search_passed=true、single_chapter_passed=true、directory_integrity_verified=false、full_reading_passed=false |
| 资源 | API健康、worker无OOM/重启，7项清理全部true |

**目录仍有缺口**：该112项呈现最新章节区和前段目录，不是已验证的完整顺序目录。尚未定位是上游分页、规则缺失还是其他兼容差异，不能把total=112当作全书章节数，也不以硬编码书名/章节数补齐。后续应对照同一目录页的分页与重复条目，补原创多页样本后再修改实现。

## 回归及复现

Windows service/parser/api普通测试通过。Linux定向集成 `.toolchain/gemini-integration-4cbcc173fe23/` 使用 `-tags integration -race -p 1 -parallel 1 -run '^Test(Legado|Content|Search|JSONField)'`，API/service/parser共18项顶层pass、0 fail、0 skip，独占无外网PG/Redis及测试容器3项清理全部true。65项契约检查、16个冻结哈希匹配，gofmt/diff和Python编译通过。没有本轮浏览器或iPhone真机通过声明。

当前源码构建Linux API/worker后，运行对应单源目录：

```text
python scripts/verify-stage3-worker-reading.py --server .toolchain/reader-compare-fixed-server-linux --worker .toolchain/reader-compare-worker-linux --sources-dir .toolchain/reader-comparison-0d21c086f2/douyin --source-name 抖音小说 --keyword 顶级气运，悄悄修炼千年 --expected-author 任我笑
```

只测搜索时追加 `--search-only`。报告passed取决于所选搜索/单章门禁，永不自动提升为完整目录、整书或发布通过。下一步优先收口抖音目录分页与重复项，再对四个网络失败源做同请求条件对照；此时不需要用户提供密码、令牌或额外个人信息。

后续已确认原源漏声明分页，建立显式适配副本取回上游13页共1202项，原源不变；修复了下一页CSS选择器链取空问题。编号重复/缺号仍作为元数据警告保留，没有自动删章/重排或把严格门禁改绿。详见 [目录分页适配验证](reader-toc-pagination-verification-2026-11-15.md)。
