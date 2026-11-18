# 搜索请求选项兼容验证

本轮未部署、未提交、未推送。公开API/DDL不变，worker保持显式启用，无浏览器过盾调用。设计范围见 [搜索请求选项设计](search-request-compatibility-design-2026-11-15.md)。

## 实现范围

原来把整个searchUrl一律当作GET地址，导致 `URL,{"method":"POST","body":...}` 的选项也进入地址。现在先有界解析后缀，再按GET或POST调用现有安全Fetcher；普通GET及相对地址行为保留。POST发送UTF-8表单、回填编码后的key/page，最多64KiB；不会把POST降级成GET。

静态头仅允许User-Agent、Accept、Content-Type；忽略空头值，不接受非空Cookie/Authorization/Host等新凭证/路由通道。重复JSON字段、忽略大小写重复头、CR/LF、未知选项、无效类型、超限体、非表单POST、GBK请求编码、动态JS地址在联网前返回unsupported_rule。现有source头来源绑定与Fetcher SSRF/DNS/重定向策略保持不变。

尚未支持的动态URL/GBK不能标成已兼容；目录和正文请求选项也不在本轮范围。

## 验证

- Windows service/fetcher普通包测试通过。新增用例覆盖UTF-8 POST和GET回填、20种拒绝输入、64KiB及回填膨胀上限、POST使用安全Fetcher一次、Get-only适配器拒绝POST。原有搜索相对URL、候选校验及模板测试仍通过。
- `.toolchain/gemini-integration-225ced322837/`：基于既有隔离脚本，使用 `-tags integration -race -p 1 -parallel 1 -run '^TestSearch|Test.*(Retry|Redirect|SSRF|Post|POST)'` 测试API/service/fetcher，14项顶层pass、0 fail、0 skip；独占PG/Redis无外网，3项清理均true。完整153项race是上一轮基线，本轮不将它冒充当前代码的完整回归。
- 此后补充Fetcher专用 `TestFetchPOSTNeverRetriesForm` 并在Windows通过：实际HTTP请求对象的方法/Content-Type/表单体保持正确，即使配置3次重试且上游503，仍仅一次调用、不进入重试backoff。它是单测，没有宣称实际外站已接受POST。
- 65项契约检查/16个冻结哈希匹配、Python编译和diff检查通过。没有前端业务修改，本轮没有重跑浏览器套件。

## 真实候选结果

只使用用户提供合集，搜索公版《三国演义》，不登录上游、不改规则制造结果、不批量下载。每次沿用无网络受限worker与独占临时数据，均未进入目录/正文。

| .toolchain证据目录 | 候选 | 结果 |
|---|---|---|
| stage3-worker-reading-396ee9c90281 | 天天看小说 | 搜索total=0、source_request_failed；当时附加结构探测也失败 |
| stage3-worker-reading-383f8f14cc70 | 爱下电子书 | 搜索total=0、source_timeout；附加探测同样受控超时 |
| stage3-worker-reading-924ffd22ec3f | 有度中文网 | 尚未请求书源；初始化数据库Unix socket服务被误判ready，建表时服务重启，基础设施失败；5项清理均true |
| stage3-worker-reading-56be5fc4ef88 | 有度中文网 | 修正探针后infra通过，POST选项已通过解析，但上游source_request_failed、total=0；未做额外结构探测 |

三次实际书源运行均api_healthy_after_source=true、worker running=true/OOM=false/restarts=0；各7项容器/IPC卷/网络清理均true。对应命令均非零、full_reading_passed=false，不能按“HTTP200聚合”认定搜索通过。

夹具修复：PostgreSQL镜像启动期间有临时Unix-socket-only实例，pg_isready默认探测可能过早成功。两个stage3脚本现均改为探测最终127.0.0.1 TCP服务，worker脚本建表也使用同一TCP目标，保留首次失败证据。结构探测改为默认关闭，显式 `--probe-json-shape` 也仅适用于普通JSON GET地址，不将POST/选项/JS地址降级成诊断GET。

第3阶段仍需有效真实书源目录/正文与iPhone15Pro真机、长时阅读等门禁。本轮没有真实书源闭环成功证据，也没有正式发布结论。
