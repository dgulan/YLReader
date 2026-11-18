# 实际前端镜像验收

本轮完成范围是部署前的本机Linux amd64前端镜像验收。未部署、未提交、未推送，未开启局域网入口；不代表正式发布或第3阶段整体完成。

## 已确认问题与修改

修复前证据 `.toolchain/ylreader-frontend-image-c2c659d88411/result.json`：HTML和SPA深链没有重验证策略，哈希资源没有长期缓存，缺失脚本返回200 HTML。原有CSP/nosniff/frame/referrer响应头及登录限流检查通过。

`frontend/nginx.conf` 新增index.html精确位置，使用expires -1生成no-cache；assets位置使用try_files返回404，成功资源expires 1y。采用expires避免子位置单独add_header覆盖父级安全头继承。API和现有SPA深链保持原路由。

前端Dockerfile改用内置解析器，避免额外获取可变docker/dockerfile:1；仍使用原先摘要固定的Node/Nginx基础镜像、npm锁文件和运行镜像安全补丁。构建上下文新增排除.env、测试、截图及报告。未从旧镜像挂载新dist或配置冒充当前构建。

## 构建与隔离

完整多阶段构建成功，日志 `.toolchain/frontend-image-fixed-build.log`。当前本地镜像 `ylreader-frontend-acceptance:20261115`，镜像ID：

`sha256:bf298f5cc9f36593cb69dc910cc708da46a0d5a40424b6bbb83b62942a7704e0`

内嵌Nginx配置SHA256 `f3a7cf4b3862882a62b167ec17fa23ef7ed29bce6a8b5ddd26cb108118a80fbe`，index.html SHA256 `cf9eb345e484ca36f39f8577385ba36981a535fbd62d20e8da7e3c75348a2507`。镜像在只读根文件系统及临时可写目录下以自身入口运行，无配置/dist绑定覆盖。

新脚本 `scripts/verify-frontend-image.py` 为每轮创建唯一标签的临时网络、PG、Redis、当前Linux API和前端容器；只有127.0.0.1随机端口，数据库不发布端口，不接入已有实例。后端为当前源码构建的Linux ELF，SHA256 `cb5d54e8c1c0c13b32627e49b2aed633fcf2a3042c9ee04245c2b1976392284a`；后端运行时用于验收，不宣称完整生产后端镜像重新验收。

## 保留的失败与范围调整

首次脚本 `.toolchain/ylreader-frontend-image-f58496820b80/` 在Docker Desktop internal网络无发布端口时停止，所有已创建对象清理成功；改用独占bridge，不能称为无网络沙箱。离线完整构建曾因缺失外部解析器或apk下载条件失败，最终正常构建成功，没有以旧镜像代替。

修复镜像首轮 `.toolchain/ylreader-frontend-image-a008bd2f2678/`：HTTP缓存、404和安全头全部通过，TXT通过；EPUB严格失败，手机参数会话在书架GET尚在途时整页进入阅读器，报告访问控制pageerror。书架load已有catch，尚不能将此报告认定为真正跨域配置错误或已定位根因。没有过滤该异常；stdout、诊断JSON及服务日志保留。

正文阅读用例改为先确认初次书架GET成功且接收完成再进入阅读器，明确其前置状态；pageerror/CSP空断言继续保留，EPUB异常收集扩大为两个会话。该调整不证明在途书架请求硬导航问题已修复，独立生命周期复核仍是待办。

同轮首次不存在账号登录探测耗时16.3秒，超过脚本15秒窗口；后端记录401，代理记录客户端中止499，因此该轮整体未通过。后续曾用缺字段的JSON对象{}验证代理限流，以429 JSON及Nginx limiting requests日志确认。后续生命周期复核发现{}仍进入dummyVerify，纠正此前“快速验证、无散列开销”的说法；当时限流结果仍成立，但没有隔离认证成本。脚本最终改用语法错误JSON并单独验证400/429，见后续生命周期认证报告。

## 安全扫描与样本

Trivy 0.76.2离线扫描本轮导出镜像，不挂Docker socket、不联网、不忽略未修复漏洞；Alpine 3.24.4共71个软件包，筛选HIGH/CRITICAL结果为0。报告 `.toolchain/security-reports/frontend-image-20261115.json`，日志 `.toolchain/frontend-image-trivy.log`。漏洞库UpdatedAt=2026-11-14T13:05:54Z，扫描时间2026-11-15T13:46:14Z；使用缓存库，不能称为实时漏洞情报或所有严重级别均无漏洞。

原创样本由 `scripts/prototypes/reading_samples.py` 确定生成，TXT使用LF、EPUB固定ZIP元数据，避免轮次间仅时间戳/平台换行造成哈希变化：

- 三章EPUB：`851b8c29e278d6ee9431c612fab89dadfab8e8da237f678df9029f951690a6d1`。
- 120章TXT：`001e925629e2bdc40aef2030f4398b64dc7f2d3cb361e2550f0e085d5cbf6cac`。

样本测试检查EPUB完整spine、成员唯一及XML可解析、两次生成一致、TXT严格1–120及正文标识。此前Windows脚本样本生成方式未改，其旧哈希不能直接与本轮LF样本比较。

## 最终复测

WebKit轮次 `.toolchain/ylreader-frontend-image-9163e8d787c2/`：2项通过、0失败，3.6分钟；EPUB实际完成60秒、38次翻章循环。14个脚本检查全部true，含两份样本确实执行、防止跳过被误计通过的诊断检查；登录探测5次429且JSON形状及代理日志符合预期。PG/Redis/API/前端/网络五项清理均true。

Chromium独立轮次 `.toolchain/ylreader-frontend-image-057542998067/`：同一镜像2项通过、0失败，1.3分钟；EPUB60秒、49次翻章循环，TXT严格目录/离线/跨会话检查通过。14个脚本检查全部true，7次429，五项清理均true。EPUB/TXT最慢单项静态资源分别484/131ms，其余资源数量与字节总计同WebKit。两浏览器共4项阅读用例通过，均要求pageerror、未处理拒绝及CSP违规为空；前置状态包含已完成初次书架加载，不包含在途书架请求硬导航。

在桌面新浏览器上下文记录到首次进入正文前17项静态资源，总transferSize=907962字节、decodedBodySize=902862字节；EPUB最慢单项192ms、TXT最慢139ms。该度量包含登录/书架/阅读模块，未记录完整可交互时间，也不是移动会话性能或p95。初次失败轮同项最高2492ms，不能据最终单次下降认定Windows WebKit冷加载延迟根因已经修复。

前端81项单测、type-check通过；Python9项离线测试、脚本编译通过；冻结契约65项检查及16个哈希匹配。完整生产构建由上述Dockerfile执行成功，没有以本机dist代替镜像构建。

复现命令（本机Docker和对应缓存运行镜像、当前Linux后端及Playwright环境为前置条件）：

```powershell
python scripts/verify-frontend-image.py --image ylreader-frontend-acceptance:20261115 --server .toolchain/frontend-image-server-linux --browser webkit
python scripts/verify-frontend-image.py --image ylreader-frontend-acceptance:20261115 --server .toolchain/frontend-image-server-linux --browser chromium
```

每轮先检查内嵌配置等于工作树，记录镜像及样本摘要。若代码发生变化必须重新构建对应产物；本地临时标签不应当作可回滚的已发布版本。

## 仍未完成

iPhone15 Pro/iOS27.2.1/Safari实际操作、至少30分钟阅读、原站目录标签/正文完整性、原生arm64及生产TLS验收仍待完成。Windows WebKit设备参数模拟不代替真机。书架在途GET硬导航异常、受限容器首次认证耗时需独立复核；本轮不宣称二者已修复。

后续已补齐书架显式取消并删除首次dummy哈希额外计算，见[生命周期与认证复核](reader-lifecycle-auth-verification-2026-11-15.md)。同时纠正阅读用例登录URL匹配误报，新精确路径检查下最新WebKit镜像轮次仍失败；本文上列通过仅代表当时夹具及产物，不能用作当前更严格场景全绿证据。
