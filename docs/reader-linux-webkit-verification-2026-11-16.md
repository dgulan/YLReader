# Linux WebKit原生引擎对照验收

同一业务产物在Linux amd64容器内的WebKit，两轮独立环境的完整原创阅读均通过，19项受控生命周期回归也通过。没有修改业务代码、API/DDL、依赖锁文件或生产镜像；未部署、提交、推送，也未开启局域网入口。Windows历史失败仍保留，第3阶段尚未完成。

## 运行环境与隔离

| 项目 | 本轮值 |
|---|---|
| 前端镜像ID | `sha256:5d9a8a3fefa4105f12fe617777018a9555a4713598d30eec3440137b0891ddcc` |
| 内嵌index.html SHA256 | `c60deba19342b6b7f07e8deafabdfd10646e9fb4d8438b7a4485b862e2533dc1` |
| Linux后端SHA256 | `8b00a2a903ff2e2471710f82d2e3e85b94270e57023fee16d7b438c38818d36e` |
| 官方浏览器镜像 | Playwright 1.65.0 Noble，amd64 digest `sha256:f6fe3cf9a493670b9a20f33597a807262abb25c40c31839ba4ac1dbc51fe7366` |
| 测试客户端/浏览器服务 | Windows客户端；服务实际platform=linux、arch=x64、Node v26.13.1、SDK 1.65.0 |
| 容器限制 | 非root pwuser；生命周期轮实际UID=1001；只读根FS、CapDrop ALL、no-new-privileges、2CPU、2GiB内存、512 PID、256MiB共享内存 |

Docker引擎为Linux/x86_64，与浏览器镜像架构匹配，未使用QEMU。这里的“原生”指Linux容器内的原生引擎，宿主仍是Windows上的Docker Desktop；不等于Linux实体设备或iOS真机。

依据[Playwright官方容器说明](https://playwright.dev/docs/docker)匹配SDK和镜像版本。只读挂载本机playwright-core纯JS包运行官方run-server，不在镜像中安装依赖，不挂Docker socket。镜像内`/ms-playwright/.docker-info`的driverVersion与SDK严格匹配后才允许测试连接。首次预检曾错误假设版本由环境变量提供，bfa36188dd52在创建业务服务前停止；已改为官方版本标记，不计为产品测试失败。

浏览器和本轮Nginx共享网络命名空间，使用容器内http://127.0.0.1访问真实前端；配置/dist未被挂载覆盖。Windows客户端通过127.0.0.1随机端口连接浏览器，路径按轮次隔离，最多一个客户端。PG/Redis/API均为独占临时服务，不接入已有数据。结束按唯一标签逆序删除浏览器、前端、API、Redis、PG和网络。

## 验收证据

| 轮次 | 范围 | 结果 |
|---|---|---|
| `.toolchain/ylreader-frontend-image-ffbf0ac096f2/` | 实际镜像+真实后端原创EPUB/TXT | 2通过，2.3分钟；EPUB60秒47次翻章；15项脚本检查全部true，六项清理全部true |
| `.toolchain/ylreader-frontend-image-f411cc940c3f/` | 实际镜像受控生命周期 | 19通过，48.1秒；19份诊断完整，15项脚本检查全部true，六项清理全部true |
| `.toolchain/ylreader-frontend-image-1a6f6ba5bc1c/` | 第二个独立环境实际镜像+真实后端原创阅读 | 2通过，1.6分钟；EPUB60秒48次翻章；15项脚本检查全部true，六项清理全部true |

两轮阅读使用不同临时账号环境、数据库、网络与浏览器进程，均为新浏览器上下文；没有清除操作系统文件缓存，不称为系统冷缓存基准。这是固定两轮复核，不是失败后重试至成功；合计4次真实阅读用例执行，仍只有EPUB/TXT两类用例，另有19种合成生命周期用例。

阅读包含桌面与iPhone15 Pro设备参数会话的续读、主题持久化、原文中文/emoji、缓存断网和重连保存；TXT目录严格1–120及首尾正文标识、多端进度复核通过。设备参数不是iPhone真机。

生命周期覆盖在途书架GET硬导航/路由取消、settings硬导航、元数据取消、预加载边界、离线检查点、授权拒绝、保存截止、丢确认、显式冲突选择及会话消失等。它使用合成API夹具，单独标记reader_lifecycle，不当作真实书源或阅读样本验收。

完整阅读时关闭浏览器内fetch/Storage/DOM/路由诊断包装，browser_instrumentation=false。15秒检查、精确同源书架地址、pageerror/未处理拒绝/CSP空断言均保留，没有预热目标模块、过滤错误或延长超时。

第一轮桌面会话首次进入正文前各记录17项资源，总transferSize=908553字节、decodedBodySize=903453字节；EPUB/TXT最慢单项分别40/20ms。只是本机受控数据，不是手机、全页面可交互时间或p95保证。

原创样本沿用前轮确定性生成器，EPUB三章SHA256为`851b8c29e278d6ee9431c612fab89dadfab8e8da237f678df9029f951690a6d1`，TXT120章为`001e925629e2bdc40aef2030f4398b64dc7f2d3cb361e2550f0e085d5cbf6cac`。没有请求外部书源。

## 工具改动与验证

`scripts/verify-frontend-image.py`新增显式Linux浏览器参数和lifecycle-only模式，记录实际平台、镜像、版本和隔离控制，继承的远程连接变量先清除，避免错误连接既有服务。所有公开控制端口验证只绑定127.0.0.1；HTTP-only与浏览器模式冲突在启动Docker前拒绝。生命周期执行数和诊断数都必须为19，新增用例时需同步审查此计数，不能把跳过记为通过。

`frontend/playwright.config.ts`保留本地Windows Chromium使用Edge，远程连接不指定Windows channel。`node frontend/scripts/verify-browser-runtime.mjs`验证本地/远程Chromium、远程WebKit配置、单worker、原15秒门禁及无额外预览服务器，已通过。

Python编译、模式冲突拒绝、diff检查通过；65项契约检查和16个冻结哈希匹配。未重跑Go全量、既有86项单测或生产镜像安全扫描，不将旧结果描述为本轮重新执行。新增官方浏览器镜像仅为测试工具，不加入生产compose或发布镜像。

## 复现与结论边界

在D:\YLReader执行，要求本机有相同缓存镜像、SDK和对应Linux后端：

```powershell
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$PSDefaultParameterValues['*:Encoding'] = 'utf8'
python scripts/verify-frontend-image.py --image ylreader-frontend-lifecycle:20261115 --server .toolchain/lifecycle-auth-server-linux --browser webkit --linux-browser-image mcr.microsoft.com/playwright@sha256:f6fe3cf9a493670b9a20f33597a807262abb25c40c31839ba4ac1dbc51fe7366
# 受控生命周期另加 --lifecycle-only
```

结果支持Windows WebKit及其运行路径与失败现象有关，但本轮同时改变浏览器服务平台、部分网络路径和资源配置，不能据一次/两次通过宣布唯一根因。Windows记录不删除、不标成已修复；Linux模拟不替代iPhone Safari、至少30分钟真实阅读、原站目录正文完整性、原生arm64或生产TLS。第3阶段整体仍未完成。
