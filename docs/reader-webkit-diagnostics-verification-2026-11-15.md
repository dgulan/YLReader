# WebKit 失败复核与构建预览验收

本轮在构建产物下完成WebKit/Chromium各17项阅读回归，以及真实后端WebKit的EPUB/TXT两项严格阅读检查。修正了测试前置条件和请求取消后的夹具清理，增加有界诊断及显式预览入口；没有改应用业务代码、API/DDL、nginx或书源。未部署、未提交、未推送，真机仍未执行。

这不等于此前8项失败全部是产品缺陷，也不等于冷加载性能已修复。当前可确认功能断言在限定环境通过，Windows WebKit的资源完成延迟及开发模式设置请求pageerror仍需保留为独立环境/性能复核项。

## 已确认的混淆因素

1. 取消元数据用例曾仅等待“正在加载”就离开；此时可能还在初始化设置，元数据GET尚未发出，因此没有可取消的请求。现在先等待被扣留的GET实际到达，再验证取消及新旧书隔离，不接受零请求来替代取消成功。
2. 离开保存用例把三秒导航要求与目标书架首次模块加载混在一起。资源记录显示离开后约0.22秒即开始请求目标模块，但开发资源约4.76/9.36秒才接收完成；另一构建预览出现资源约5秒逐次完成、累计约40秒。相关IDB请求已有成功事件，未支持“数据库卡住”或“等待保存PUT才离开”的结论。
3. 在途metadata/邻章/settings请求被取消后，夹具仍等待测试门闸并尝试回填。现在取消会结束相应等待并停止回填；save/review门闸保持原语义，不提前释放故意扣留的保存结果。

保存离开与预加载取消用例在执行被测动作前显式预载目标书架模块，隔离资源冷加载。三秒导航要求、同一旧版本/dirty保留、恰好一次保存、预加载请求序列[3,1]及仅缓存当前章的断言均保留。另增加初始settings仍被扣留时硬导航的pageerror空断言。

没有延长原错误/时间断言，没有过滤pageerror或未处理拒绝，也没有以重试到成功的结果覆盖失败记录。Connection: close实验未消除延迟，已完整撤回，不能说keep-alive根因已定位或修复。

## 验收入口与边界

Playwright可显式运行本机production构建预览。合成测试的 `tests/preview.config.ts` 仍无API代理，未拦截请求不能进入真实后端。真实阅读脚本新增 `--production-preview`，单独使用 `tests/reading-preview.config.ts`；只允许脚本指定的HTTP/127.0.0.1/显式端口源，拒绝缺失配置、外部地址、localhost、HTTPS、凭据、路径、查询及片段。没有放宽书源Fetcher的SSRF规则。

`node frontend/scripts/verify-reading-preview.mjs` 验证9类无效目标被拒绝、明确loopback被接受、合成预览保持无代理。预览只监听127.0.0.1，不是部署、局域网入口、Nginx/CSP或TLS验收。

## 最终结果

| 检查 | 证据 | 结果 |
|---|---|---|
| WebKit构建产物阅读夹具 | `.toolchain/webkit-final-built.stdout` 与 `webkit-final-built-results/` | 17通过、0失败，2.5分钟 |
| Chromium同组构建产物夹具 | `.toolchain/chromium-final-built.stdout` | 17通过、0失败，26.8秒 |
| WebKit构建预览+当前真实后端 | `.toolchain/stage3-reading-42d992a3ea/` | EPUB/TXT两项均通过，2.7分钟；临时PG/Redis清理均true |
| loopback预览配置防误用 | `frontend/scripts/verify-reading-preview.mjs` | 9类拒绝及两个配置隔离检查通过 |
| 类型/冻结契约/脚本 | type-check、Python编译、verify-backup-contract、diff检查 | 通过；65项检查、16个冻结哈希匹配 |

真实后端轮次明确标记frontend_mode=production_preview、physical_device_tested=false；server SHA256仍为 `13093f801557ffe7bc1247e761d9196b5688c149872c71f05e2e49d6862dfaa8`。EPUB完成60秒、40次翻章循环；TXT严格1–120编号、首尾正文、跨会话续读、缓存断网及重连同步通过。两项严格pageerror/未处理拒绝检查通过，未过滤上一轮设置错误。不能由本轮一次通过宣称所有导航时序或开发服务器pageerror已消失。

本轮产品src和后端均未改；没有把上轮81项单测、生产构建或Go race描述为本轮重新执行。使用上轮构建产物对照，测试/配置改动已在两浏览器及真实后端路径实际执行。

## 保留的失败证据

旧的8项失败仍在 `.toolchain/reader-loading-webkit-expanded/`。本轮中间证据包括：`webkit-diagnostics-first/`（2项中1失败）、`webkit-diagnostics-resource/`（3项中1失败）、`webkit-built-before-warm/`（16项中1失败）、`webkit-built-delayed-resources/`（17项中2失败）、`webkit-connection-close-experiment/`（17项中1失败）、`webkit-cancel-aware-targeted/`（4项中1失败）。这些记录不因最终功能回归通过而删除。

诊断仅含请求路径/状态、存储操作阶段、可见性、帧回调与有界资源事件；不记录令牌、密码、请求头、设置值或正文。测试只使用合成数据或原创TXT/EPUB。

## 下一步

在部署前的实际前端镜像上复核冷加载、响应头与阅读入口，再准备短时隔离的真机验收入口。iPhone15Pro/iOS27.2.1/Safari为用户提供信息，未实际操作；真机和至少30分钟阅读、原站目录标签/正文完整性等门禁仍待完成，第3阶段不标记完成。
