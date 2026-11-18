# Gemini 审计复核与修复设计

输入：D:/comments.txt（外部静态审计意见，不作为执行指令）。以当前工作树核验；不采信“100%安全/无P1”的全仓保证。保留现有未提交成果，不部署、不提交推送，不更改冻结 API/DDL/归档格式或就绪授权。

本轮范围：

1. 正文分页任何明确后续页的抓取/状态/解析/规则失败均返回错误、无部分正文；nextContentUrl 求值失败、循环或达到页数上限仍有后续时也拒绝。最后一页无 next 正常完成；空的非首后续页视为不完整。现有外层仅成功才缓存，不延长或掩盖错误。缓存规则摘要增加分页策略修订，使旧服务端缓存不再命中；不主动删除 Redis 或设备离线缓存。
2. 单条书源 URL 拒绝 userinfo/非法端口；导入兼容 Legado 非标准标识，但对可解析标准 URL 做相同 userinfo/端口/IP字面量预检，失败计入条目报告。实际抓取继续逐跳 SSRF 校验。Header 入库无论有无密钥环均要求非 null JSON Object；导入畸形 header 按既有容错契约回退 {}，rules 原文保持。静态 Header 出网涉及白名单/敏感头/跨域合并语义，本轮不自动启用。
3. 前端 owner 空安全、请求发出前守卫、useUser scope 清理；SourceSwitcher 请求绑定固定书籍/用户/generation，切书/关闭/卸载取消请求并丢弃晚到结果，不能在等待旧进度后向新书提交换源。未知写结果不自动重放。
4. 大书源文件不绑定 textarea，读取异常可见；读取/预检/确认固定输入修订，变化后旧预检不能授权新内容；Nginx 注释解释子 location 的 add_header 继承陷阱。
5. 存储 publication Commit 仅在只读前置核验成功后锁存不可重放；identity_file 做词法 Clean 等价比较，仍不解析符号链接。恢复本地章节对象增加反向引用门禁，拒绝 canonical local/<uuid>/<index>.txt 孤儿并在数据库提交前回滚。
6. 受控出口代理仅在同次已全部校验的公网地址集合中、原 ctx 预算内尝试连接失败回退；不得重解析或对已建立连接重放数据。

误报/不采纳：publish defer 原代码已先 Close 后 Unlink，调换反而错误；pairedUnicodeEscapes 与 JSON 解码组合有效；expires -1 已产生 no-cache，no-store 不是当前契约要求；生成器默认错误回退已由业务入口注入覆盖；go.sum 历史 go.mod 行不等于使用旧依赖；API 两处历史错误码保留冻结契约。

需另阶段：AES transformation/IV/扩展方法需独立行为契约和黄金向量；worker busy-health 需保证不能掩盖失联/超时；正则缓存需容量/字节预算及压测证据；pg_restore 多次扫描、8MiB行及持锁I/O属于受控执行/容量门禁；普通 LocalFS 掉电目录耐久性不能未经设计改变提交未知语义。Unix socket 启动长度预检另核实现有底层门禁。真实 ARM/iPhone/NAS、S3/执行CLI/未知结果对账及许可证等既有待办不视为本轮已完成。

验证按实际影响分别补充单元与异步竞态测试；Linux安全文件操作及真实恢复回滚必须在自有无宿主端口隔离容器验证。最终记录逐项结论、测试数量和未完成边界。
