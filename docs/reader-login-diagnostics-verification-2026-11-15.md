# WebKit登录后导航诊断

本轮最终只保留测试诊断与验收脚本变更，没有修改应用src、后端、依赖、Nginx或基础镜像；不部署、提交、推送。保持15秒登录/导航检查、精确书架目标、页面异常及CSP空断言，不预热模块或自动重试。

## 已定位的范围

当前同一amd64镜像仍为 `sha256:5d9a8a3fefa4105f12fe617777018a9555a4713598d30eec3440137b0891ddcc`，后端SHA256仍为 `8b00a2a903ff2e2471710f82d2e3e85b94270e57023fee16d7b438c38818d36e`。本轮未重建或扫描新镜像。

有效失败轮 `.toolchain/ylreader-frontend-image-0acbaa59ddbe/` 和 `ebc5ba4f5b59/`：登录200、JSON解析完成、sessionStorage写入成功、路由beforeEach进入/bookshelf；但没有beforeResolve/afterEach，停在异步组件解析。静态资源responseEnd很快，与此前仅凭代理日志认为下载慢的判断不符；不能归因于密码验证或真正跨域拒绝。

独立登录复现 `.toolchain/ylreader-frontend-image-45bca90afe7f/`：仍按15秒严格失败。登录200到fetch兑现约5秒，书架资源下载31–44ms，但Playwright requestfinished及样式load被延迟分批观察；原断言失败后的诊断快照发出后，CSS load和模块解析随即完成。失败后原生import探针resolved，不作为预热成功或阅读通过证据。尚不能凭这一轮确定浏览器/驱动/事件调度的最底层根因。

Playwright本机源码显示，函数式toHaveURL使用waitForURL，而完整URL字符串进入服务端to.have.url检查。改用完整同源/bookshelf字符串，拒绝/login?redirect=/bookshelf，并保持15秒。本次是测试等待方式对照，不是已证实的业务修复。

## 正反对照与限制

- 完整URL `.toolchain/ylreader-frontend-image-6c658b06b9ee/`：桌面/移动两个冷上下文登录通过，单项测试29.3秒；200到route-afterEach分别14560/8905ms。
- 反向谓词 `.toolchain/ylreader-frontend-image-31920162bede/`：也通过，24.8秒；分别11757/8781ms。不能据此声称谓词等待必然触发或更换断言已根治问题。
- 浏览器内诊断可能改变时序。因此完整阅读模式显式关闭fetch/Storage/DOM/路由包装，仅在login-only诊断模式启用；后续必须在关闭包装下验证。

`--login-only`报告verification_mode=login_navigation，只代表登录，不包含原创阅读验收。完整模式为reading；HTTP-only与login-only互斥，谓词对照只允许诊断模式。公开的[Playwright请求事件文档](https://playwright.dev/docs/api/class-request)区分响应与请求结束；本轮结论以同轮客户端和代理证据为准，没有将外部相似问题认定为本项目相同根因。

## 诊断可靠性与保留记录

首次追加样式探针错误地在head创建前访问document.head，导致初始化pageerror；`.toolchain/ylreader-frontend-image-9a85555c0482/`作废，不当作业务失败或性能样本。保留 `login-diagnostics-invalid-head-results/`。现改为观察Node.prototype在head存在时追加link，既有错误不被过滤；该探针仅用于诊断模式。

诊断每页最多300条，仅记录路径、阶段、状态码、会话存在布尔值和静态资源时间；不记录密码、令牌、JSON正文、用户标识或查询参数。既有pageerror收集仍严格保留。

新增测试模块的独立tsc尝试因本机缺少@types/node未完成，不宣称该尝试通过；不为诊断引入额外依赖。实际Playwright编译和运行结果作为本轮测试代码验证，应用既有type-check另行记录。

真机、至少30分钟阅读、原站目录正文完整性及生产TLS仍未完成，第3阶段不标记完成。Windows WebKit单轮通过不能替代iPhone15 Pro/iOS27.2.1/Safari实际验收，偶发完成事件延迟仍须复核。

## 关闭包装后的验收与隔离

`.toolchain/ylreader-frontend-image-8a8e83e50621/`关闭浏览器内包装，完整URL和15秒检查保持：TXT通过，EPUB在首次正文加载15秒内未完成而失败；两项登录已通过。因此问题并不限于登录，此轮仍不能报告全绿。所有HTTP/安全头/400与429限流检查通过，五项资源清理均true。失败页面与诊断保留 `login-uninstrumented-reading-results/`。

新增原创loopback复现脚本 `scripts/prototypes/webkit-network-repro.mjs`：纯HTML/JSON/CSS/8模块样例不含Vue、数据库或业务；WebKit谓词/完整URL分别69/68ms，Chromium44/119ms，四项通过且服务关闭，证据 `.toolchain/webkit-network-repro-7b9ac419-9ca/`。最小样例没有复现秒级延迟，不能将问题泛化为所有WebKit网络调用。

`--app-fixture`以当前构建产物和模拟API替代Nginx、Go、PG/Redis，服务只监听loopback：`.toolchain/webkit-network-repro-49a65dc4-bbb/`中WebKit谓词/完整URL导航8655/4263ms，Chromium1/5ms，四项通过。WebKit初始页面到登录请求还分别耗时5042/4338ms；延迟仍能在前端构建与该引擎组合出现，但该夹具未使用实际代理的全部缓存/安全头，不据此排除所有代理影响。

管理UI合并chunk曾作为实验构建，日志 `.toolchain/login-management-chunk-build.log`：产生887.27kB（gzip283.19kB）的管理块，并把原本其他管理页面成本提前到登录页。该候选被拒绝、Vite配置已完整撤回；不以挪动加载成本换取登录后的时间，也未构建或发布该实验镜像。原构建保存在 `.toolchain/login-baseline-dist/`，随后按原配置重新构建。

## 最终状态

关闭浏览器内包装的Chromium实际镜像轮次 `.toolchain/ylreader-frontend-image-d482707248d8/`：原创EPUB/TXT两项通过，1.3分钟；verification_mode=reading、browser_instrumentation=false，15项脚本检查全部true，五项资源清理成功。当前严格URL检查没有回退为查询参数匹配。

WebKit最终完整轮仍是8a8e83e50621：1通过/1失败，不能把独立登录或模拟API通过当作真实阅读全绿。现有证据将重点缩小到当前引擎与实际前端模块/事件完成时序；尚未定位最底层原因，没有证据支持为此修改认证、放宽CSP、降低密码成本或宣称所有问题属于环境误报。

撤回候选后生产构建与应用type-check通过，日志 `.toolchain/login-restored-build.log`；当前index.html字节与原构建快照一致（SHA256 `c60deba19342b6b7f07e8deafabdfd10646e9fb4d8438b7a4485b862e2533dc1`），Vite配置无差异。Python脚本编译、最小复现脚本语法检查通过；两个易混淆CLI模式组合均在启动Docker前拒绝。既有86项单测和Linux认证race是前轮证据，本轮没有重跑，不叠加为本轮测试数量。

下一步优先在另一原生平台运行相同构建/原创样本与现有严格门禁，并补充事件调度时间对照；iPhone入口仍未开启。不要继续用重复到偶然成功的轮次覆盖已保留失败，不以管理包合并或提高时间预算替代定位。

2026-11-16后续：相同镜像与后端在Linux amd64 WebKit下，两轮独立真实阅读均为2通过，另19项生命周期通过，见[Linux WebKit验收](reader-linux-webkit-verification-2026-11-16.md)。该结果没有覆盖或删除本文Windows失败，也不等于唯一根因已确定；实际平台和部分网络路径有差异，真机/长期阅读仍待验。
