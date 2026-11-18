# 抖音小说目录分页适配验证

本轮未部署、未提交、未推送，未修改用户原书源或reader运行实例。设计及两项规则差异见 [分页设计](reader-toc-pagination-design-2026-11-15.md)。

## 112项的原因

原配置 `ruleToc.chapterList=id.list@dd`，`nextTocUrl`为空。元数据探测 `.toolchain/stage3-worker-reading-3753b041372c/` 显示13个分页选项（1–100至1201–1202）和明确下一页链接；原首页前12项为最新1192–1181章，其后为第1–100章，合计112项。

`.toolchain/stage3-worker-reading-a105950b02bb/` 额外只读核对了第二页：两页都有“作品正文”分区，下一页分别指向第2、第3页。没有读取正文。现有服务在空nextTocUrl时不自动猜页，因此不是112项硬上限，也不能由用户原截图推定旧reader已收集完整目录。

## 适配副本与真实结果

基于原始源创建明确命名的“抖音小说（目录分页适配）”，仅修改chapterList、nextTocUrl和用于区别的显示名称，其他规则不变。原始单源SHA256：`0b7d4ce58d7afe2288716d50e09ee23fd4e70d1c01099bd13a65d32fa2aa5969`；适配副本SHA256：`32a2598c49396560942931b54ab1b70a751dad079ce2d3dccb2581c5f2cc99a3`。

本地完整JSON：`.toolchain/reader-comparison-0d21c086f2/douyin-pagination-adapted/source.json`；差异清单位于同级父目录 `douyin-pagination-adaptation.json`。这些测试数据保持忽略，不把上游整份配置加入源码审计包。报告均显式标注rules_modified=true，不能把适配副本结果当成原配置通过。

两轮元数据验收：

| 证据目录（.toolchain内） | 实际结果 |
|---|---|
| stage3-worker-reading-fe1b3a841bff | HTTP200，8.981秒，1202项/1202个唯一URL，首项第1章、末项第1192章；严格编号期望不符，门禁false |
| stage3-worker-reading-3f70a40a5882 | HTTP200，8.003秒，同样1202项/唯一URL；补充编号诊断，门禁仍false |

第二轮结构统计：1200项有阿拉伯数字章号、2项无此章号；唯一章号1190个，范围1–1192有2个编号未出现，编号非递减但不严格递增。重复章号为200、251、261、442、618、622、628、722、736、972，每个各出现两次；不同URL有的同标题、有的不同标题。

**不自动删章或重排**：章号重复可能涉及重发、分段或标注差异，只凭目录标签无法判断正文重复或丢失。已保留上游顺序及所有不同URL。此次实际取齐了上游声明的1202项，但directory_integrity_verified仍false，因为预先设定的严格编号检查未通过；没有降低断言来制造通过，也没有读这些章节正文。single_chapter_passed/full_reading_passed均false（本轮只做目录元数据）。

两轮均在既有10秒目录预算、20页上限内完成，没有扩大预算、关闭TLS或放宽SSRF；API健康、worker无OOM/重启，各7项容器/卷/网络清理均true。既有“后续页失败保留前序结果”语义没有改动，未来慢网络仍可能只返回部分目录，不能凭HTTP200判定完整。

## 引擎修复与测试

发现并修复真实引擎缺陷：EvalNextURL的CSS分支原来一次选择整个字符串，没有像列表求值一样展开@链，导致 `.pager@a.next@href` 返回空。现在复用splitSelectorChain逐段选择，保留引号/属性值内@语义；XPath/JS分支及网络策略未改。

新增原创样本测试：嵌套分页容器/带@属性/Legado tag索引下一页规则；两页都有最新区时只取正文并按序合并；循环链接只请求两页；未声明分页时仍不自动翻页。CSS链用例先失败后通过。初版适配使用逆向兄弟轴嵌套谓词，在当前执行路径中漏掉样本第2章，改为正向following-sibling规则后仍按原先3章断言验证通过；该组合差异未进一步归因，不宣称已修复所有XPath兼容问题。

Windows parser/service/api普通测试通过。Linux定向race集成 `.toolchain/gemini-integration-a0ab47c9114d/`：`-tags integration -race -p 1 -parallel 1 -run '^Test(TOC|NextURL|EvalNextURL)'`，API/service/parser共8项顶层pass，0 fail、0 skip，3项专属PG/Redis/测试容器清理true。65项契约检查、16个冻结哈希匹配，gofmt/diff及Python编译通过。

## 使用范围及待办

适配副本与原源具有相同bookSourceUrl，显示名称不同不代表可以在同一账号无冲突并存。当前仅在空的临时测试库导入新书；没有覆盖用户数据。目录index=0已从原配置的第1192章变为适配后的第1章，直接重排已有书籍目录可能改变旧进度/书签含义，尚未验证这些数据的按URL重定位，不能自动推广覆盖已有阅读账号。

后续优先补同源目录重排时的进度/书签保护、慢网络部分刷新语义，再核对源站编号异常。分页适配可作为隔离测试配置，不表示第3阶段或正式发布门禁已完成。

后续进展：同源目录状态保护已完成原创夹具和隔离数据库回归，见[目录状态保护验收](reader-toc-state-verification-2026-11-15.md)。这不替代真实112→1202目录迁移演练，也未解决本站章节编号异常；仍不能自动覆盖已有阅读账号。

复现目录元数据验收（命令会因严格编号检查未通过而非零退出，属于保留的真实结果）：

```text
python scripts/verify-stage3-worker-reading.py --server .toolchain/toc-pagination-server-linux --worker .toolchain/reader-compare-worker-linux --sources-dir .toolchain/reader-comparison-0d21c086f2/douyin-pagination-adapted --source-name 抖音小说（目录分页适配） --source-variant explicit_rule_patch --keyword 顶级气运，悄悄修炼千年 --expected-author 任我笑 --directory-probe --expected-directory-count 1202 --expected-numbered-chapters 1192
```
