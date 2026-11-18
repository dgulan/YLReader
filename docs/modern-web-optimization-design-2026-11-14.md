# Modern Web 优化范围（2026-11-14）

参考 GoogleChrome/modern-web-guidance 的原生 dialog、navigation-drawer 和 responsive-design 指导，结合现有 Frozen v1.5 契约，仅改前端资源加载、原生控件和目录交互。研究、OpenAPI、DDL、备份格式及恢复流程保持不变，不部署、不引入新依赖、不执行远程安装脚本或复制参考项目代码。

## 本轮交付约定

1. Element Plus 完整样式仅由使用其组件的管理/外围路由导入，随路由异步加载；直接打开已登录阅读器不加载该样式。保持核心阅读器原生实现。以相同工具链的生产构建记录入口 CSS 前后体积，并用生产预览网络请求验证分包结果，不将压缩体积变化等同于 Core Web Vitals 实测。
2. 原生 dialog 保持 showModal 的浏览器焦点约束、Escape 和关闭后焦点返回；补充明确初始焦点、模态滚动隔离、有限高度和动态视口/安全区。保留书签/换源忙碌时的 cancel 防护，不增加自制焦点陷阱或覆盖 Escape 行为。
3. 虚拟列表保持最多 13 行 DOM 和固定 44px 行高，增加方向键、Home/End、PageUp/PageDown 导航及 roving tabindex，跳过禁用项。键盘只移动焦点，Enter/Space 经原生按钮选择；大目录打开时显示当前章节，选项范围改变时修正焦点和滚动。添加完整列表位置语义，限制移动端列表高度。
4. 阅读主题同步 color-scheme，让原生 select 与夜间背景一致；正文/导航适配安全区，小屏标题让位于操作按钮。聚焦样式使用主题颜色，不压缩触控按钮。对现代 CSS 提供基本布局回退，不引入实验性 API 强依赖。

验证包含现有前端单测、strict 类型检查、生产构建及独立 API mock 的真实浏览器测试。新增用例覆盖千行目录键盘导航、模态焦点返回、夜间原生控件、小屏布局，以及生产资源分离；不连接业务数据库，不冒充远程书源、真机、Safari/Firefox 或性能跑分验收。

参考：

- https://github.com/GoogleChrome/modern-web-guidance
- https://github.com/GoogleChrome/modern-web-guidance/blob/main/skills/modern-web-guidance/guides/ui-components/dialog.md
- https://github.com/GoogleChrome/modern-web-guidance/blob/main/skills/modern-web-guidance/guides/ui-components/navigation-drawer.md
- https://github.com/GoogleChrome/modern-web-guidance/blob/main/skills/modern-web-guidance/guides/css/responsive-design.md
