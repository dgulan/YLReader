# 阅读进度检查点约定（2026-11-14）

沿用 Frozen v1.5、If-Match 版本冲突和 IndexedDB 位置结构，仅修改前端保存流程，不新增 API、持久字段或依赖，不部署。

1. visibilitychange 到 hidden 以及 pagehide 时，取消滚动 debounce、捕获当前可见位置并尝试保存。先尝试 IndexedDB 检查点，再发版本化请求；不采用缺少 Bearer/If-Match 契约的 sendBeacon，不增加 unload/beforeunload 阻塞。卸载时移除监听。
2. 同时触发的滚动、手动、生命周期或离开保存共享一个 Promise；失败返回后不会因为其他等待者而自动重放。新的显式触发可重试；单次任务内部仍用 revision 合并请求期间的新位置。
3. 每次保存固定用户、书籍和视图世代，在异步边界重新核验。退出、换用户、重新启动或卸载后的旧确认/错误不能修改新视图或继续发请求。已经发送的服务端写入可能已提交，不把丢弃迟到 UI 更新称作回滚。
4. 区分本机检查点失败、服务端请求失败和服务端确认后本机更新失败。服务端成功立即采用其版本；后续本机失败不能被描述为服务端未保存。重试本机记录时，若位置未再变，不重复 PUT。离线提示只有在本机记录写入成功时才声称“保存在本机”。
5. 未解决的 409 保持原始基线版本及本机位置，仍须用户明确选择；不自动授权覆盖远端。

浏览器可冻结/终止页面，生命周期事件及异步 IndexedDB/网络均不保证在强制关闭或移动端杀进程前完成。本轮改善事件到来时的尽力保存，不承诺最终关闭零丢失。测试中的 hidden/pagehide 注入须明确标记为模拟事件，不冒充系统级挂起测试。

依据：[MDN visibilitychange](https://developer.mozilla.org/en-US/docs/Web/API/Document/visibilitychange_event)、[MDN pagehide](https://developer.mozilla.org/en-US/docs/Web/API/Window/pagehide_event)。
