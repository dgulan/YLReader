# 书架生命周期与首次认证复核

本轮不改变冻结API/DDL及密码参数，未部署、提交、推送，未开启局域网入口。保留工作树既有修改，没有合并PR。

## 修复与失败证据

`BookshelfView`原先只有响应序号保护，没有向列表GET传AbortSignal，也没有在卸载、硬导航及新筛选到达时取消。真实Vue组件回归的修复前结果为3失败/1通过，见 `.toolchain/bookshelf-lifecycle-red.log`。修复后每个列表请求独立控制，替换/卸载取消并使序号失效，迟到结果不能覆盖列表；pagehide取消，bfcache pageshow.persisted重新读取，不重放写操作。普通失败仍显示，预期取消不显示错误。

仅pagehide版本的WebKit硬导航没有观察到显式取消，路由导航通过。改为只在GET在途时临时注册beforeunload提前取消，完成、替换、取消、卸载后移除；不调用preventDefault，不显示离开提示。测试并不证明移动进程终止时一定运行事件，也未测bfcache准入率。

`.toolchain/bookshelf-webkit-built-final.log` 为中途停止的19项运行，前8项含1失败，不作为全组通过。其间错误启动复测产生端口冲突，记录在 `.toolchain/bookshelf-webkit-early.log`；重叠进程均已停止，最终改为串行。中间结果保留在 `.toolchain/bookshelf-webkit-interrupted-results/`。

提前取消后专项首轮 `.toolchain/bookshelf-webkit-targeted.log` 仍为1失败/1通过：书架显式取消已成功，但另一个在途settings GET发生访问控制pageerror。证据在 `.toolchain/bookshelf-webkit-settings-interference-results/`；不将其过滤，也不称全局网络异常已解决。

最终专项夹具先确认settings成功且接收完成，只扣留书架GET，确保隔离被测请求。WebKit两项通过，37.9秒，见 `.toolchain/bookshelf-webkit-isolated.log` 和 `bookshelf-webkit-isolated-results/`；严格验证发出请求含信号、导航触发显式取消、目标正文成功且pageerror为空。初次settings在途硬导航仍保留既有独立用例，不能用本轮书架专项替代它或声称设置生命周期已全面修复。

## 认证

不存在账号首次调用原先在sync.Once中生成dummy哈希，再进行VerifyPassword，首次额外消耗一次Argon2；随机数失败的fallback尺寸无效，会快速退出。改为公开固定的有效dummy PHC，每次仍执行与默认真实用户相同的m=65536,t=3,p=2、16字节盐、32字节key校验，结果始终忽略。未降低真实用户密码强度；账号不存在仍返回401。

Linux无网络容器race单测通过，见 `.toolchain/auth-dummy-race.log`：校验dummy参数、盐/key尺寸、正常执行不认证及不安全PHC边界。常量不关联任何真实账号或秘密，不是测试凭据泄露。

修复前独占2CPU/512MiB后端对照 `.toolchain/ylreader-frontend-image-7f5d4f42d631/`：缺失用户首次1.458秒、后续0.101秒，已存在用户错误密码0.091秒、正确密码0.107秒；状态401/401/401/200，全部临时资源清理。此前共享主机轮次的16.3秒不能完全归因于dummy初始化；本轮不将单次数字当作认证性能SLO。

修复后首轮 `.toolchain/ylreader-frontend-image-e8f36dae8776/`：同配置、浏览器启动前，缺失用户首次1.268秒、后续0.087秒，已存在用户错误0.094秒、正确0.103秒，状态符合预期。虽然消除了首次额外哈希，仍有首次延迟，不能声称16.3秒问题已完全定位或有确定的倍数改善。

该轮实际阅读2项失败，原因是登录断言仅检查URL包含bookshelf，/login?redirect=/bookshelf也满足；移动会话登录POST尚在途就被page.goto取消，目标仍在登录页。诊断与页面快照在 `.toolchain/reading-login-path-race-results/`，不是正文解析或目录丢失。现改为精确检查url.pathname为/bookshelf，保留所有页面错误和阅读断言，没有恢复书架GET完成的临时屏障。

精确路径后 `.toolchain/ylreader-frontend-image-b78acc88b680/`：EPUB通过，60秒43次循环；TXT仍在移动会话登录阶段失败，代理未记录该次登录POST，未执行到正文，不能写为TXT阅读通过。初次认证0.750秒、后续0.103秒，错误/正确密码0.121/0.111秒，状态符合预期；五项资源清理均true。失败资料另存 `.toolchain/reading-login-foreground-results/`。

后续用例在操作每个会话前显式bringToFront，并在15秒窗口内确认实际登录POST成功，再检查精确路径；不自动重试点击、不等待书架GET、不延长原错误窗口。登录响应状态加入诊断，不记录请求体或令牌。该调整明确真实交互会话及认证前置条件，尚不能据一次结果断言上述未发请求的根因已定位。

该调整后的WebKit轮次 `.toolchain/ylreader-frontend-image-895ac47729aa/` 仍严格失败：两项均记录登录200，但精确路径未在15秒内离开/login，均未进入阅读阶段。当前新镜像不能报告WebKit真实阅读全绿。证据另存 `.toolchain/reading-webkit-strict-login-results/`；五项临时资源清理成功。Nginx日志记录登录后约5秒才开始下一批书架模块请求并返回200，尚缺浏览器端模块完成/路由守卫/会话写入的联合证据，不能断言只是网络、CSP或密码校验问题。

精确路径检查比旧字符串匹配实际更严格；历史通过不能证明此前15秒内真实登录跳转稳定。保留此门禁，不再增加等待书架完成、预热目标模块或自动重试来获得绿色结果。下一步应对同一轮收集不含令牌的会话存在布尔值、路由阶段、模块请求开始/完成时间，定位登录200之后的等待。

验收脚本另纠正一处假设：登录body={}仍进入dummyVerify，无法隔离密码成本。最终代理限流探测使用语法错误JSON，新增要求未限流请求为400、限流为429，继续核对JSON与Nginx日志。前述历史{}轮次的429事实保留，但不再称其无密码散列开销。

## 其他检查与边界

前端16个测试文件共86项单测、type-check、生产构建通过。新增5项组件单测覆盖卸载取消、pagehide/恢复、替换筛选/迟到结果、普通失败和提前卸载时取消但不阻止导航。Python9项离线测试及脚本编译通过；65项契约检查与16个冻结哈希匹配。

Chromium构建产物全组19项生命周期回归通过，26.0秒，见 `.toolchain/bookshelf-chromium-final.log` 和 `bookshelf-chromium-final-results/`。包含两项新增书架取消，以及原有settings硬导航、元数据取消、缓存授权边界、离线/丢确认/冲突/会话消失等用例。WebKit本轮只有两项书架专项最终通过，不把中断的全组轮次写为19项通过。

当前最终镜像Chromium真实阅读 `.toolchain/ylreader-frontend-image-633c15e6ac3c/`：两项通过，1.2分钟；EPUB60秒51次翻章，TXT严格120章/首尾正文/跨会话/断网恢复通过。采用精确登录路径、实际登录200及显式激活会话；未等待书架GET完成。15个脚本检查全部true，语法错误登录探测4次400、8次429；五项临时资源清理成功。仍要求pageerror/未处理拒绝/CSP违规为空。

| 当前证据 | 结果 |
|---|---|
| Vue组件及既有前端单测 | 86通过 |
| Linux认证参数/PHC race | 2通过 |
| Chromium构建产物生命周期 | 19通过 |
| WebKit隔离书架在途GET硬导航/路由导航 | 2通过 |
| Chromium最终实际镜像原创阅读 | 2通过，脚本15项检查通过 |
| WebKit精确登录路径下实际镜像原创阅读 | 最新轮2失败，未进入阅读；前一轮EPUB通过/TXT登录阶段失败 |

因此当前不能报告全绿，也不能以Chromium或书架专项通过替代WebKit完整闭环。

实际镜像已完整构建，最终构建日志 `.toolchain/frontend-lifecycle-final-build.log`。新前端镜像ID为 `sha256:5d9a8a3fefa4105f12fe617777018a9555a4713598d30eec3440137b0891ddcc`；新Linux后端SHA256 `8b00a2a903ff2e2471710f82d2e3e85b94270e57023fee16d7b438c38818d36e`。中间仅pagehide镜像未作为最终验收产物。

原阅读用例已撤回上轮“等待初次书架GET完成”的临时前置条件，新镜像按原导航顺序复测结果如上，WebKit仍有严格门禁失败。iPhone真机、至少30分钟持续阅读、原站目录正文完整性、原生arm64及TLS仍未完成，第3阶段不标记完成。本轮没有重新扫描最终新镜像，不把上轮镜像安全扫描当作本轮产物结果。

后续在不改业务的同镜像下增加联合诊断，确认会话写入成功，等待位于异步模块/事件完成；完整URL和谓词反向对照均能通过，故未宣称某个断言方式是唯一根因。关闭包装后最新WebKit为TXT通过、EPUB首次正文超时，Chromium两项通过，见[进一步诊断](reader-login-diagnostics-verification-2026-11-15.md)。仍不能将历史单轮通过视为当前WebKit稳定闭环。
