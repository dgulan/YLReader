# Modern Web 优化验收（2026-11-14）

本轮按 [范围约定](modern-web-optimization-design-2026-11-14.md) 优化阅读器入口资源、长目录键盘交互和原生模态/移动布局。参考的是 [GoogleChrome/modern-web-guidance](https://github.com/GoogleChrome/modern-web-guidance) 中的原生 dialog、导航抽屉、动态视口和可访问性指导；结合 YLReader 自行实现，没有安装远程包、复制其示例代码或引入新依赖。已有 Vue、Element Plus 管理页面、Dexie 和恢复工作均保留。

## 资源结果

同一 Node 26.13.0、Vite 6.4.5、现有依赖下，对修改前后分别执行生产构建。下表为 Vite 输出的十进制 KB，gzip 为构建估算，并非线上传输测量。

| CSS 范围 | 修改前 | 修改后 |
|---|---:|---:|
| 全局入口 | 369.11 KB | 5.82 KB |
| 阅读器异步样式 | 5.98 KB | 7.64 KB |
| 直接打开已登录阅读器合计 | 375.09 KB | 13.46 KB |
| 上述合计 gzip | 51.75 KB | 3.85 KB |

阅读器首次 CSS 约减少 96.4%；入口实际文件 5,817 字节，阅读器 7,644 字节。Element Plus 的 363,291 字节 CSS 由使用其组件的八个管理/外围路由显式导入，Vite 提取成共享异步资源。本次构建其文件名为 dayjs-CGjERCMX.css；文件名由分包算法产生，不表示它只有日期样式。

真实生产预览中，以全新浏览器上下文直接打开带合成会话的阅读器，样式表不含 .el-button；导航到书架后新增 CSS 请求且 .el-button 样式存在。避免仅凭构建文件名推测成功。登录/书架本来就需要 Element Plus；先访问这些页面再进入阅读器时，该样式已经加载，不宣称此路径也节省了同等流量。键盘交互增加了少量阅读器 JS，未声称总 JS 变小，也未测量或承诺 LCP/INP/CLS 跑分。

## 交互结果

- 虚拟列表保留固定 44px 行高、最多 13 个按钮，使用原生按钮选择。方向键、Home/End、PageUp/PageDown 只移动焦点，Enter/Space 才触发选择；禁用卷标题被跳过。支持完整列表位置语义及单一 Tab 入口，手动滚动后入口随可见窗口修正。
- 千章目录附带一个禁用卷标题，打开时展示当前第 500 章，向下跳过卷标题；End 可到第 1000 章，Home 返回第 1 章，PageDown/PageUp 正确移动。Enter 后真实路由视图切换到第 1000 章。全过程 DOM 不膨胀到千行。
- 目录初始焦点为关闭按钮；Escape 后浏览器恢复目录入口焦点，重开仍定位当前章节。书签弹窗的关闭/焦点返回也通过。原有换源和书签 busy 时 cancel 防护继续保留，未新增自制焦点陷阱。
- 使用原生 :modal 和 ::backdrop 滚动隔离；目录高度受动态视口限制。320×640 下主要操作均在视口内且触控高度至少 44px，812×375 横屏下目录关闭按钮和当前章节仍可见。
- 夜间 color-scheme=dark、日间 color-scheme=light，使原生 select 匹配主题；选中目录与聚焦轮廓使用阅读器主题文字色。正文、页头、页脚增加安全区间距，小屏长书名不挤出操作按钮。

## 检查与证据

- npm run type-check 通过。
- npm test：11 个测试文件、52 项单测通过，包含新增禁用项/边界导航测试。
- npm run build 通过；最终日志 `.toolchain/modern-web-build.log`。
- 最终生产预览 Edge：3 个浏览器用例通过、0 失败/跳过/重试，日志 `.toolchain/modern-web-playwright.json`。新增用例位于 frontend/tests/modern-web.spec.ts；全部 API 均为测试拦截，无真实数据库或账户。
- 保存并检查 `.test-artifacts/modern-web-mobile-night.png`、modern-web-mobile-dialog.png 和 modern-web-directory.png（均在 frontend 下）。320px 截图中没有操作越界，目录当前项与关闭按钮完整可见。
- 65 项契约检查通过、16 份冻结摘要匹配；git diff --check 通过。源码/依赖锁未发生后端契约修改。

浏览器复现：在 frontend 执行构建并启动绑定 127.0.0.1 的 npm run preview，设置 E2E_BASE_URL 为该预览地址、E2E_PRODUCTION=1，然后执行 `npm run test:e2e -- tests/modern-web.spec.ts`。不设置 E2E_PRODUCTION 时资源分包用例会明确跳过，其余两个交互用例可在开发服务运行。

本轮没有重跑依赖真实后端的历史 11 项浏览器场景，没有宣称 Firefox、Safari、真机安全区、屏幕阅读器或 Core Web Vitals 已验收。没有部署、提交、推送或合并，也没有将当前备份恢复 validating 状态升级。
