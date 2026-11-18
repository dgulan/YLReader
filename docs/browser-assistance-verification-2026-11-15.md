# 浏览器验证页诊断与FlareSolverr建议复核

范围依据browser-assistance-design-2026-11-15.md；未部署、安装或启动FlareSolverr，未修改默认通道、公开API/DDL、书源元数据或Redis缓存。接入门禁仍是设计草案，不是已实现的solver适配器。

## 本轮交付

- Fetcher新增固定ErrChallengeRequired，errors.Is仍匹配既有ErrUpstreamError，因此API继续使用502/source_request_failed，不新增错误码。搜索聚合failure提供固定中文“书源站点要求浏览器验证”，不回显Cookie/HTML/URL。
- cf-mitigated: challenge在任意状态下识别，立即关闭响应。403/503仅在Cloudflare标识和技术性正文特征组合时回退；非HTML声明、cf-ray、普通禁止访问、普通维护页、小说中的普通短语不触发。5xx最多查看4KiB，因此不能保证识别所有旧验证页。
- 已识别的503验证页不再进行普通幂等直连重试。正常503保留三次总尝试；POST验证页仍只执行一次。不新增solver请求、不把挑战页送给正文/搜索规则，不改变SSRF/重定向/域名及源限流。

新增10个分类子测试验证状态、误报、尝试次数与响应关闭；API测试核对错误码及敏感文本不泄露。Fetcher/API/Service全包回归及go vet通过，最终日志 `.toolchain/challenge-regression-final-20261115.log`。首轮受限执行环境阻止部分既有回环socket测试；未改测试或放宽SSRF，改用获得授权的本地测试执行后通过，失败输出不冒充成功。

## 实际地址抽样

复用用户两个合集里四个此前测试过的无JS搜索地址，查询三国演义；调用生产Fetcher，不执行规则或solver，不保存正文。每地址10秒预算，原始URL矩阵只保存在忽略目录。显式integration探针默认跳过，只有TEST_PUBLIC_CHALLENGE_URLS指定时运行，最多8个URL。

记录 `.toolchain/challenge-public-probe-20261115.log`：猫眼HTTP200、Server nginx、cf-mitigated空；9x阅读器、得奇小说网、快书网为上游失败，均未识别为明确挑战。普通5xx/网络错误仍包装为upstream，不能据此断言站点没有Cloudflare或归因于解析错误。本结果没有证明这三个站点可由FlareSolverr修复；不能将所有source_request_failed升级为过盾任务。

## 对建议的结论

可保留默认Go抓取、按需浏览器、UA配对、缓存与防击穿方向。必须修正：可靠挑战标志、精确origin与租户/出口隔离、Cookie实际到期、进程内singleflight与分布式锁的区别、10秒搜索预算与60秒浏览器预算冲突、全局资源上限、GET单次重放及浏览器所有子资源的SSRF边界。

Cookie/UA/IP一致只是必要条件之一；官方Cloudflare资料说明clearance绑定访客/设备且可提前失效，不能承诺Go重放必定成功。浏览器辅助默认关闭，不因源JSON字段自动启用；本轮未加入cf_mode=auto/always。所有后续浏览器流量应通过独立出网约束，Go首次URL检查不能覆盖浏览器内部跳转、脚本或子资源。

备份65项检查及16份冻结摘要、git diff --check作为本轮收尾校验；不宣称重跑了全量浏览器或真实过盾实验。下一步应先落实浏览器出口隔离与模拟适配器契约测试，再选明确遇到挑战、且允许测试的来源做真实兼容验收。
