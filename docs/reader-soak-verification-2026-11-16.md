# 连续阅读稳定性验收记录

当前状态：最终独立运行已通过连续1802秒自动化阅读验收；先前失败和中断均保留，不累计时长。未部署、提交、推送或开启局域网；没有修改应用src、后端或生产镜像。该结论不等于iPhone真机或第3阶段整体完成。

## 范围与实现

依据[长测设计](reader-soak-design-2026-11-16.md)，新增独立reader_soak模式，不修改原60–600秒冒烟范围。主试验使用真实单调时钟，持续阅读区间不包括初始化或收尾；不累计短轮次、不加速时钟。整体测试预算扩大，单步15秒断言保持。

同一移动参数浏览器会话连续翻章、滚动与保存，计划第5/15/25分钟三次缓存断网恢复，定期主题/字号、视口变化及只读桌面书架核对；最后停止移动操作后在桌面打开阅读器复核续读。桌面观察阶段不同时打开阅读器写进度，避免人为制造多写者冲突。必须观察两会话各至少一次应用自动令牌刷新，不手工续期、不记录令牌。

每个循环保存原子心跳；父进程每分钟采样本轮五个容器的内存/CPU/PID。最终退出码、持续时间、操作计数、续期、终点续读、异常为空、无OOM/重启及清理共同决定通过。资源采样不是峰值、Go堆或浏览器JS堆分析，不足以证明不存在内存泄漏。

## 已完成的夹具与门禁验证

独立60秒probe轮次 `.toolchain/ylreader-frontend-image-34cbe670275b/`通过：实际连续60.88秒、12次翻章、3次断网恢复、2次桌面核对、4次主题/字号变化、2次视口变化及终点续读。pageerror、未处理拒绝、CSP违规为空；没有续期事件，符合短试验范围。该轮明确标记reader_soak_probe，不能作为30分钟或令牌过期后的证据；全部临时资源清理成功。

初期结果门禁新增7项单测，连同既有脚本测试16项通过，覆盖完成标记不能替代真实时长、短模式不能冒充长模式、缺少断网/跨会话/续期、错误、缺字段、NaN/Infinity等假成功。后续再补凭据注入及资源完整性检查，本轮最终脚本测试共20项通过。CLI拒绝1799秒长测及probe与长模式混用，均在启动Docker前停止。脚本编译与diff检查通过。

## 保留的未完成运行

首个长测目录 `.toolchain/ylreader-frontend-image-5fe34cd900ec/`，心跳为soak-heartbeat.json，资源为memory-samples.json，最终结果以result.json及reader-soak-diagnostics-0.json为准；该轮明确未通过。

首轮实际连续903.32秒，60次完整循环、2次断网恢复、2次桌面核对、7次主题/字号和3次视口调整。移动会话自动refresh已返回200；桌面观察错误地断言首个GET响应必须成功，在正常401中间响应处立即终止，未等待应用续期/重试。不能据该轮认定桌面续期存在产品缺陷。completed=false、整体passed=false，全部临时资源清理；失败页面另存reader-soak-first-5fe34cd900ec-results，不将15分钟累计到后续长测。

修正为15秒内等待书架最终200，同时记录所有书架响应，并继续要求刷新成功和服务器读位正确。短probe新增明确的access凭据损坏验证该边界，长模式禁止注入；对应新增门禁单测。当前脚本测试合计17项通过，首次7项门禁测试之后又新增1项“人工损坏不能证明真实到期”。

修正后的probe `.toolchain/ylreader-frontend-image-54efd48cc161/`连续62.29秒通过：12循环、3次断网、终点续读成功，桌面实际观察401→自动refresh200→重试200，desktop_refreshes=1；auth_injection=true，仅为协议夹具验证，不能当作真实到期。

第二个长测环境890d2cfb6040在279.29秒主动中断，非产品失败、不计入累计时长，六项资源清理成功。原因是进一步核对发现，收尾重复保存同一读位可因dirty=false不发请求，独立API核对可能恰逢第二次到期。夹具改为每次在40%/55%之间实际滚动，使应用正常保存请求承担续期；未手动刷新长测令牌，也未放宽401或异常断言。中断证据保留reader-soak-interrupted-890d2cfb6040-results。

最终夹具probe089f51726995运行60.13秒通过，12循环、3次断网、2次桌面核对、桌面刷新1次及终点续读；仍为auth_injection=true的短协议验证，不计作真实到期或30分钟。其结果及旧probe均独立保留。

## 最终连续运行

目录 `.toolchain/ylreader-frontend-image-a3183e450e7d/`。阅读开始于2026-11-16 08:57:54（Asia/Shanghai），连续单调计时1802.00秒；初始化、终点续读和清理不用于凑足1800秒。完整用例含准备与收尾约30.5分钟，退出码0、整体passed=true。

| 项目 | 实际结果 |
|---|---|
| 连续翻章/滚动保存 | 120个完整循环，覆盖末章并返回前章 |
| 缓存断网/重连 | 3次，计划在第5/15/25分钟 |
| 桌面书架核对 | 5次，服务器读位对应 |
| 主题/字号变化 | 14轮，每轮确认真实PATCH成功 |
| 横竖尺寸切换 | 5轮，正文及布局断言通过；不是物理旋转 |
| 应用自动续期 | 移动2次、桌面2次，均HTTP200；auth_injection=false |
| 收尾续读 | 停止移动操作后桌面打开正确章节且恢复章内滚动位置 |
| 异常 | pageerror、未处理拒绝、CSP违规均为空 |
| 容器状态 | 五个容器仍运行，无OOM、无重启 |
| 清理 | 浏览器、前端、API、Redis、PG、网络六项全部true |

约第15分钟和收尾再次到期时均观察到自动续期。桌面记录含401→refresh200→重试200，没有人工请求refresh，没有改服务器有效期、加速时钟或过滤页面异常。

共31组资源采样。收尾补充了“每组五容器齐全、字段有效且时间单调”的独立验证，31组全部有效，记录于resource-completeness-check.json；原始result.json未改写。当前脚本内置该加强门禁，并增加拒绝空采样、缺失/重复容器、缺字段和重复时间的单测，不仅按原始采样条数计通过。

| 容器 | 最小采样MiB | 最大采样MiB | 末次采样MiB |
|---|---:|---:|---:|
| 浏览器 | 72.79 | 678.20 | 668.10 |
| API | 10.35 | 137.60 | 10.99 |
| 前端 | 5.15 | 5.77 | 5.40 |
| PostgreSQL | 76.80 | 90.04 | 89.84 |
| Redis | 3.39 | 3.97 | 3.93 |

数据含准备阶段，最大采样值不是实际峰值或泄漏诊断。原始memory-samples.json及派生resource-summary.json均保留。

前端仍为 `sha256:5d9a8a3fefa4105f12fe617777018a9555a4713598d30eec3440137b0891ddcc`；后端SHA256仍为 `8b00a2a903ff2e2471710f82d2e3e85b94270e57023fee16d7b438c38818d36e`。Linux浏览器使用官方Playwright 1.65.0 Noble amd64摘要 `sha256:f6fe3cf9a493670b9a20f33597a807262abb25c40c31839ba4ac1dbc51fe7366`，非root、只读文件系统与原资源预算不变。

原创120章TXT沿用确定性样本，SHA256 `001e925629e2bdc40aef2030f4398b64dc7f2d3cb361e2550f0e085d5cbf6cac`。没有运行远程书源，不代表EPUB也已做30分钟长测。

## 复现

在D:\YLReader使用相同缓存产物：

```powershell
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$PSDefaultParameterValues['*:Encoding'] = 'utf8'
python scripts/verify-frontend-image.py --image ylreader-frontend-lifecycle:20261115 --server .toolchain/lifecycle-auth-server-linux --browser webkit --soak-seconds 1800 --linux-browser-image mcr.microsoft.com/playwright@sha256:f6fe3cf9a493670b9a20f33597a807262abb25c40c31839ba4ac1dbc51fe7366
# 夹具预检将 --soak-seconds 1800 换成 --soak-probe，不能混用。
```

自动化滚动、视口变化和setOffline不是物理触摸、旋转、锁屏或蜂窝网络。iPhone15 Pro/iOS27.2.1/Safari真机清单仍未执行；第3阶段整体不能由本试验独自关闭。
