# 第3阶段阅读验收记录

本阶段已开展本地真实后端、原创中文样本、外部书源抽样及跨引擎验收，**尚不代表第3阶段全部关闭**。未部署、提交、推送、合并；所有用户、书籍、进度、令牌和数据库均为本轮专有测试数据。

## 环境与样本

当前源码Windows后端、PostgreSQL16、Redis7、Vue开发服务器和Playwright浏览器。服务种子账户/密钥随机生成，结果不保存凭据。数据库只发布本机回环随机端口，结束按所有权标签清理容器与匿名卷；后端进程同时终止。不存在指向已有书库的测试配置。

原创TXT由既有reader.spec生成：5章中文、emoji、每章100段；新增EPUB3包含mimetype、OPF、导航、3章中文与emoji。并未使用参考仓库或复制商业小说。桌面及独立移动尺寸会话通过真实登录、导入、书架、目录、正文和进度API操作。

完整Edge/Chromium套件记录 `.toolchain/stage3-reading-8819e0182e/`：**30通过、1跳过、0失败**，2.6分钟，容器清理均true。跳过项为要求生产preview的管理CSS按需加载测试；不能冒充生产CSP、生产镜像或Safari测试通过。该轮早于相对搜索URL修复；后续当前源码API/Service测试包含新增搜索回归并通过，外部搜索另外实测。

新增EPUB用例无HTTP路由模拟：桌面与另一独立会话登录、导入、第二章进度落库、移动会话读取同一位置、主题重载保持、一分钟翻章/断网/重连、重连设置API验证。已有套件还覆盖TXT精确位置、多端版本冲突显式处理、离线章节、退出清缓存、千章目录虚拟化、书签及书籍管理；其中在线搜索/目录故障等部分测试使用模拟HTTP，不能视作真实站点兼容性证明。

最终使用当前后端（包含相对搜索URL修复）、iPhone15Pro参数和严格pageerror/unhandledrejection断言的Chromium单用例复核通过，记录 `.toolchain/stage3-reading-e55014746e/`，清理均true。此单用例不与之前30项重复相加作为31个独立用例。

## 实际修复与夹具校正

- 真实书源发现相对searchUrl未以bookSourceUrl解析，导致合法相对路径误报unsafe_url。现使用既有resolveURL后再调用原Fetcher；保留URL/DNS/私网/重定向保护。单测覆盖根路径、相对目录、跨域绝对地址及私网地址仍交由安全抓取器拒绝。
- 原创本地导入首轮500来自测试后端继承了外部TMP/TEMP；脚本现创建专属临时目录，重跑导入201。没有修改产品错误响应来隐藏失败。
- 两项SQL夹具原依赖宿主psql，现支持E2E_DB_USER/E2E_DB_NAME并使用本轮数据库容器内的客户端，不依赖生产库或宿主安装。
- 旧换源用例要求502触发致命告警，未考虑此前同书源缓存恢复能力。现同时断言确实收到502、继续显示源B正文、仍能换源及旧源缓存已失效，不删除错误注入。
- WebKit后台页会暂停帧推进，自动化在页面交互前显式bringToFront，不将对不可见页面的点击视作真实用户操作。

失败记录 `.toolchain/stage3-reading-f70fe3c7d8/`、`c840c1b23c/`、`c18eb87c83/` 均保留；后两轮分别为缺psql及旧断言，清理均完成。

## 用户提供书源

- [71e56d4f合集](https://legado.aoaostar.com/sources/71e56d4f.json)：22条，173127字节，SHA256 `24fe11c400ddc8754703ff0c4d8a249baf024da3a15563de805cc134140959a0`。
- [4dc410d1合集](https://legado.aoaostar.com/sources/4dc410d1.json)：128条，901291字节，SHA256 `05da3da45910c8342d4935044c9af6298be0fbcee62af910b6aea2550fee44de`。

两份原始JSON只保留在忽略目录，不进入源码或审计资料包。真实API导入预览分别报告22/128导入、0失败、0跳过。**可导入不等于规则全部可运行。** 无自动上游登录、验证码处理或付费内容访问。

仅抽样无JS搜索规则，清空静态认证header，以“**三国演义**”进行顺序受限搜索。修复前4例均unsafe_url；修复后 `.toolchain/stage3-reading-0d04c58fbe/external-source-results.json` 显示：

| 书源 | 实测结果 |
|---|---|
| 猫眼看书 | 历史HTTP200、total=1、0失败码、约0.514秒；后续确认候选书名/URL为空，此项撤回“有效搜索成功”结论 |
| 9x阅读器 | 聚合HTTP200，0结果，source_request_failed，约1.176秒 |
| 得奇小说网 | 聚合HTTP200，0结果，source_request_failed，约1.594秒 |
| 快书网 | 聚合HTTP200，0结果，source_request_failed，约0.009秒 |

聚合HTTP200并不表示单源成功。三个上游失败不直接归因为解析引擎缺陷；没有绕过SSL/SSRF、伪造成功响应或放宽超时。该目录整体browser_exit=1，原因是单独WebKit运行事件断言失败，不能把该轮整体写成通过；外部搜索JSON的测量结果独立有效。

猫眼的目录规则包含JS，本轮Windows API没有独立worker接入，因此**没有继续执行该真实JS目录/正文链**。其他含JS规则也未在父进程试跑；需在受限Linux worker环境继续真实书源目录/正文验收。没有用模拟搜索替代这一缺口。

## iPhone 15 Pro 与未关闭门禁

用户确认手机为iPhone15Pro，iOS/Safari版本尚未记录。测试增加Playwright iPhone15Pro设备参数与可选E2E_BROWSER=webkit，安装锁定Playwright对应WebKit27.2 Windows端口。它不是实际iOS Safari，不能替代真机。

一分钟翻章/断网冒烟不是长时阅读稳定性证据。Windows WebKit运行遇到后台帧暂停、强制断网的settings访问控制资源事件；失败保留在 `.toolchain/stage3-reading-ee49a46a58/`、`0d04c58fbe/`、`b2769569cd/`。最后一次 `.toolchain/stage3-reading-bfbc1e7d12/` 又在移动会话打开时停留loading、15秒内未显示章节。当前没有充分证据将这些问题只归因于仿真器；**WebKit兼容门禁未通过**。

诊断期间曾尝试区分断网资源事件与脚本错误，最终没有保留豁免：测试继续要求pageerror和unhandledrejection均为空，同时记录失败请求数，并要求恢复后真实设置API返回主题night。不靠忽略访问控制事件或放宽断言来宣称Safari通过。所有运行的临时数据库/进程均清理；Safari兼容仍需进一步诊断及真机复核。

仍待：真实iPhone15Pro上记录iOS/Safari版本；受控测试地址；合法样本导入、字体/主题、刘海/横竖屏/软键盘遮挡；锁屏/切后台/返回页面、Wi-Fi与蜂窝切换；离线与重连后的进度核对；至少一次较长真实阅读；受限worker下真实书源目录/正文、ARM原生与生产CSP兼容。不能据本轮结果宣称正式发布或完整试用门禁已满足。

真机记录表：

| 检查 | 待填写结果 |
|---|---|
| iOS版本 / Safari版本 / 日期 | 待填 |
| 授权的隔离测试地址与样本摘要 | 待填；本轮临时服务已停止，不提供已失效端口给手机访问 |
| 导入TXT/EPUB、章节/emoji是否正确 | 待验 |
| 桌面第二章→手机继续阅读→桌面复核 | 待验 |
| 日/夜主题、字号、横竖屏、软键盘、顶部安全区 | 待验 |
| 切后台/锁屏后恢复、断网已缓存章、重连无回退 | 待验 |
| 至少30分钟实际阅读及异常记录 | 待验，30分钟为后续计划，非本轮完成证据 |

## 复现

先构建当前源码Windows后端至忽略目录，然后运行：

```text
python scripts/verify-stage3-reading.py --server .toolchain/stage3-server-final.exe
```

WebKit：安装锁定版本的`npx playwright install webkit`，设置E2E_BROWSER=webkit后加`--reading-only`。外部测试另加`--sources-dir <本地JSON目录>`，只需外部部分时加`--sources-only`。脚本清除继承的E2E_BASE_URL/worker端点，绑定本轮凭据与数据库，避免复用既有后端；报告中的passed为执行流程结果，外部完整阅读仍单独标记未通过。

## WebKit诊断与设置同步修复（后续复核）

本轮使用当前源码重建 `.toolchain/stage3-server-safari.exe`。增加请求路径/状态、失败原因、异常栈和操作阶段的JSON诊断，不记录请求头、令牌、密码或响应正文；隔离脚本将其复制到每轮独立证据目录，避免下一次Playwright运行覆盖。

首次诊断 `.toolchain/stage3-reading-c7acf4db67/` 严格失败并完整保留。异常时间为主题更改后立即reload期间，早于首次offline；栈位于 `api/client.ts request` 与 `readerConfig.ts` 的设置同步。这纠正了此前将其称为“强制断网settings错误”的归因。该证据提示与重载中断在途设置请求有关，不能据此断言Safari本身存在CORS配置缺陷，也不能证明已定位所有首次加载卡住问题。

主题持久化用例改为明确等待真实PATCH成功且响应theme=night，然后重载；不再将未确认写入与已确认持久化混为一步。pageerror和unhandledrejection仍严格要求为空，没有错误过滤。未确认修改的保留和重试继续由单测独立覆盖。

产品修复：设置GET/PATCH通过已有withDeadline限制为15秒并向fetch传递AbortSignal。读取超时解除settings初始化等待、保留本机偏好、显示可重试提示；写入超时保留pending，不因超时立即重放。两项新增单测模拟永不完成及迟到响应，确认中止信号、loading/saving释放、迟到结果不覆盖、pending持久化及重试最新修改。该修复针对设置网络等待，不承诺所有IndexedDB或书籍请求都已有截止时间。

修复后 `.toolchain/stage3-reading-fe01d17c69/`：Windows WebKit27.2桌面及iPhone15Pro设备参数会话严格通过1项真实后端阅读测试（1.8分钟），覆盖原创EPUB导入、跨端第二章续读、主题确认保存后重载、一分钟翻章和12次断网/重连、恢复后设置API主题一致。诊断0条pageerror、最终0条unhandledrejection；记录21条预期断网/取消请求，不将它们当作成功HTTP。专属PG/Redis清理均true。

前端79项单测、type-check、生产构建通过；65项契约检查/16个冻结哈希匹配，Python脚本编译与diff检查通过。当前只能记为“WebKit本轮冒烟通过”：历史首次加载偶发卡住仍需更多有界诊断、实际iOS和较长阅读复核。第3阶段仍未完成，真实JS书源目录/正文、生产CSP及真机门禁维持待验。

首次Chromium完整回归 `.toolchain/stage3-reading-6d5dc7bc63/` 为29通过/1失败/1跳过，不能报告全绿；本轮原创阅读场景本身通过。失败位于“核对响应到达前会话消失”用例：reviewing按钮在本机持久化完成前就进入disabled，测试随即删除会话，产品的会话检查可能在发GET前直接返回，测试却一直等待响应。修正夹具为实际GET抵达拦截点后才删除会话并放行响应，继续断言无冲突、无额外PUT、原进度版本和dirty保留；没有改变产品会话检查或放宽安全断言。该轮临时数据库清理均true。

夹具修正后的完整Chromium回归 `.toolchain/stage3-reading-36fc127c02/`：30通过/1条件跳过/0失败（2.3分钟），包括该会话消失用例和原创跨端阅读。跳过项是要求生产前端的管理CSS延迟加载检查，本轮为Vite开发服务器，不算生产CSP或镜像验收通过。专属PG/Redis清理均true。最终type-check及diff检查通过。本轮未部署、未提交、未推送。

## 受限worker真实书源后续复核

已在独占Linux API与无网络受限worker下复核猫眼，发现前次total=1实际为空书名/空URL候选，不能算有效书籍搜索。已修复候选校验与JSON相对URL模板预检，修复后真实搜索total=0；上游结构探测有一次成功、一次受控超时，未进入真实目录/正文。完整记录与复现见 [worker真实书源复核](stage3-worker-reading-verification-2026-11-15.md)。第3阶段完整门禁保持未通过。

用户提供reader运行截图并确认书名/作者后，已按同一本书复现抖音小说搜索9条有效结果，并修复Legado对象正文规则读取遗漏，单章正文HTTP200。目录当前112项含最新章节区及前段目录，尚未验证完整性；第3阶段整体仍未完成。详见 [截图对照与兼容修复](reader-screenshot-comparison-verification-2026-11-15.md)。此前《鬼吹灯》作为书名的失败不代表截图中的同名书源普遍不可用。

后续显式分页适配在隔离环境取得1202个不同URL，但编号重复/缺失，严格目录完整性门禁仍失败，详见[目录分页验收](reader-toc-pagination-verification-2026-11-15.md)。本轮进一步完成同源目录重排的进度/书签事务保护、过期抓取校验及正文缓存URL校验，定向Linux race回归31项顶层通过，详见[目录状态保护验收](reader-toc-state-verification-2026-11-15.md)。尚未在真实旧目录上演练已有账号迁移，未自动推广适配源；部分分页失败发布语义、陈旧客户端新建引用与iPhone真机仍待闭环，第3阶段保持未完成。

分页失败发布语义现已补齐：已知错误、空后续页、循环与页数耗尽均不再作为完整目录发布，失败前后持久状态快照一致，重试仍可成功。隔离Linux race定向回归35项顶层通过，见[分页失败保护验收](reader-toc-completeness-verification-2026-11-15.md)。此轮没有复跑真实远程源；严格末页语义可能要求适配源调整规则，真实迁移、编号完整性及iPhone真机仍待验。

后续真实复验：严格分页语义下第13页正常结束，取得1202个唯一URL，无需改规则。两轮临时账号112→1202目录迁移通过，进度0→1201、书签0→1201及12→0，字段保留、旧版本PUT409；详见[真实目录迁移验收](reader-toc-migration-verification-2026-11-15.md)。编号完整性门禁仍false，整体命令非零退出，未将迁移成功当作完整阅读成功；编号异常、长期阅读及实际iPhone验收尚待完成，第3阶段仍未完成。

编号异常后续归因：非数字项为两条原站目录记录，未出现721/970数字标签；四个原站目录页的有界标题探测与API对应样本一致。尚不能证明YLReader丢章、正文重复或正文缺失，不自动删章或改号，严格编号门禁仍false；见[目录编号异常复核](reader-toc-label-audit-verification-2026-11-15.md)。第3阶段仍需编号正常的合法样本及iPhone真机/长时间阅读验收。

新增原创120章TXT已验证正常编号、虚拟目录、跨会话及断网缓存，并修复online与旧失败保存重叠时丢失重试意图。Chromium全量32通过/1条件跳过，前端81项单测通过；但扩大WebKit检查8通过/8失败，真实后端WebKit EPUB仍有设置请求pageerror，不能报全绿。见[正常编号与重连保存验收](reader-numbered-sample-verification-2026-11-15.md)。用户补充真机为iPhone15 Pro/iOS27.2.1/Safari，[真机清单](reader-iphone15pro-checklist-2026-11-15.md)已准备，实际未执行；第3阶段继续保持未完成。

WebKit后续复核：已区分元数据尚未发出、目标路由冷加载及取消后夹具继续等待等因素，保留原功能断言，增加构建产物预览入口。最终WebKit/Chromium同组阅读夹具各17项通过；真实后端WebKit构建预览EPUB/TXT两项通过。详见[WebKit诊断与构建预览验收](reader-webkit-diagnostics-verification-2026-11-15.md)。未改应用业务代码，不能宣称Windows冷加载延迟或开发模式settings错误均已修复；实际镜像、真机和长期阅读仍待验，第3阶段未完成。

实际amd64前端镜像后续验收：完整Dockerfile构建，未挂载覆盖配置或dist；发现并修复HTML缓存策略和缺失脚本返回200 HTML的问题。WebKit/Chromium已加载书架后的EPUB/TXT各两项严格通过，HTTP缓存、404、安全头与代理限流检查通过；当前镜像离线扫描HIGH/CRITICAL为0（使用2026-11-14缓存库）。首轮在途书架GET硬导航pageerror及首次认证耗时16.3秒仍列独立待复核，不以调整正文阅读前置状态称其已修复。详见[实际前端镜像验收](reader-frontend-image-verification-2026-11-15.md)。未部署/提交/推送，真机、长期阅读、目录正文完整性仍未完成，第3阶段保持未完成。

书架请求/认证后续：补齐书架GET替换、卸载、硬导航取消及恢复只读加载，消除首次dummy哈希额外计算，未降低密码成本。86项前端单测、Linux认证race、Chromium19项生命周期及WebKit两项隔离书架取消回归通过。但阅读用例原先用URL包含bookshelf误把/login?redirect=/bookshelf视为已登录；改为精确路径和登录200后，最新WebKit实际镜像两项均在登录后跳转15秒检查失败。已保留失败，不以历史通过或书架专项替代真实闭环；见[生命周期与认证复核](reader-lifecycle-auth-verification-2026-11-15.md)。第3阶段仍未完成，下一步定位登录200之后的模块/路由等待，另保留settings在途硬导航异常。

登录导航进一步隔离：同轮证据确认JSON、会话写入和路由beforeEach成功，等待位于异步组件/事件完成；完整URL与谓词反向对照都能通过，未证明断言方式是唯一根因。关闭浏览器内诊断后，当前WebKit完整真实镜像TXT通过、EPUB首次正文15秒失败，Chromium两项通过；见[登录与模块诊断复核](reader-login-diagnostics-verification-2026-11-15.md)。纯HTML最小样例快速通过，同前端构建+模拟API的WebKit仍有秒级等待；没有修改业务或放宽安全策略。管理包合并实验因提前增加登录下载成本撤回。第3阶段继续保持未完成，需其他原生平台及真机对照。

2026-11-16 Linux对照：同一前端镜像和后端、相同Playwright 1.65.0版本，在Linux amd64容器内原生WebKit上，两轮独立原创阅读均2通过，另19项生命周期全部通过；严格超时、CSP和页面异常断言未放宽，所有临时资源清理成功。详见[Linux WebKit验收](reader-linux-webkit-verification-2026-11-16.md)。Windows已有失败保持原状，不能据对照唯一归因于Windows；iPhone15 Pro/iOS27.2.1/Safari真机、至少30分钟真实阅读及原站目录正文完整性仍未完成，第3阶段不标记完成。

2026-11-16自动化长测：Linux WebKit同一移动参数会话以真实时钟连续1802秒通过，120循环、3次断网、5次桌面核对、14轮主题/字号、5轮视口变化；移动/桌面各自动续期2次，最终章内续读通过，异常为空，无OOM/重启且清理成功。见[连续阅读验收](reader-soak-verification-2026-11-16.md)。首轮15分钟夹具失败及一次主动中断单独保留，不累计为30分钟；人工/物理iPhone30分钟阅读仍未执行，原站正文完整性等门禁仍待验，第3阶段整体未完成。
