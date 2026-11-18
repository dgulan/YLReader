# 阅读器加载与取消约定（2026-11-14）

在 Frozen v1.5 前端实现范围内优化加载，不修改服务端 API、DDL、进度 code point 语义或 IndexedDB 版本，不部署。

发现：fetchChapter 在 await 后使用响应式 bookId 写缓存；切书时旧结果可能落到新书键下。boot 的元数据请求没有取消信号，旧响应可能覆盖新路由状态。四个邻章并发也可能占用当前阅读请求的资源。

本轮固定如下内部约定：

- 每次章节读取固定 owner/book/context/index/signal，所有缓存读写使用同一快照；在请求、缓存读写等异步边界检查取消。取消不进入离线降级，也不显示网络错误。
- boot 元数据使用独立 AbortController，重新打开或离开时取消；每个 await 后先检查信号，再修改视图。章节请求维持独立生命周期，重开章节取消旧章节和其预加载。
- 当前正文展示后预加载 ±1/±2，优先下一章，相邻任务最多两个并发；单个失败不阻止其他任务，取消后不启动剩余项。保留现有 IndexedDB 五章窗口和仅网络失败使用缓存的行为，不变成默认读取过期在线缓存。
- 已开始的 IndexedDB 操作不能由 AbortSignal 回滚，但其键必须始终属于原始请求，不能改写到另一书籍。暂不声称撤销已经完成的网络或数据库工作。
- 码点长度以流式遍历计数；UTF-16 定位只遍历所需前缀，不为整章创建 Array.from/slice/join 数组。保留现有 DOM Text 的 WeakMap 偏移索引和二分定位，不增加无限全局正文缓存。

通过单测覆盖取消、晚到响应、不可变缓存键、离线分支及并发上限；真实浏览器用合成 API 验证迟到章节/元数据与路由切换、预加载取消和离线恢复。基于数组分配减少和请求调度事实报告收益，不冒充完整 INP 或真机性能跑分。

取消实现依据原生 AbortController/AbortSignal：[MDN AbortController](https://developer.mozilla.org/en-US/docs/Web/API/AbortController)、[throwIfAborted](https://developer.mozilla.org/en-US/docs/Web/API/AbortSignal/throwIfAborted)。
