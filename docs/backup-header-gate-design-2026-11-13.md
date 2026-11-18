# 恢复 header 门禁及对象发布边界（2026-11-13）

实现前确定：新增内部 RunDatabaseWithHeaders 路径，显式传入专用 header keyring；不使用进程默认 keyring、环境覆盖或 age 私钥充当 header 密钥。既有仅数据库原语继续用于隔离诊断，不能作为发布授权。

keyring 文件是私有权限文件，UTF-8 严格 JSON：keyring_format=ylreader-header-keyring、keyring_version=1、current_kid、keys（kid 到标准 base64 32 字节密钥）。所有字段必填，拒绝重复/未知字段及重复 kid；文件至多 64 KiB、1–64 个 key。kid 使用现有 manifest 语法。此为内部加载能力，不修改冻结配置格式或加入命令行明文密钥参数。

manifest 声明的 kid 必须全部有密钥。加载数据后、会话安全版本更新和 COMMIT 之前，逐行检查 book_sources.header：空对象允许；非空对象必须恰好一个 __enc 字符串，版本 v1、kid 在 manifest 中。用现有 AES-GCM 实现与该行真实 user/source UUID 的 AAD 解密，明文必须是有效 UTF-8 JSON 对象。检查全部行而非只检查名字或部分样本，实际 kid 集合必须精确等于 manifest 清单；未知版本、混合信封、非空历史明文、错误密钥、AAD 错误及清单漂移均拒绝。

单个信封 JSON 上限 128 KiB、解密 JSON 上限 64 KiB，是当前隔离实验的资源上限，不冒充所有历史数据的兼容保证。明文只存在于本轮检查缓冲，尽力清空后释放，不写文件/结果/日志；Go 运行时不提供可承诺的物理安全擦除。任何底层解密错误必须归一为不含 header、密钥、用户、书源或 kid 值的稳定错误。

错误 keyring 或缺 key 在写入前拒绝。已加载事务中的解密/清单错误使整个事务回滚，并沿既有结果登记规则处理对象；unknown 不能自动清理。成功仍停留 validating，不能因 header 通过就产生 ready_for_cutover。结果重放只返回已登记状态，不表示再次检查密钥或生成发布凭证。

对象发布后续必须将 opaque-id 暂存布局转换成原 key 业务布局，通过已验证目录句柄逐层不可覆盖创建、逐对象回读摘要及目录同步后发布独立完成记录。现有普通 LocalFS.Put 可覆盖写，不能直接拿它实现灾备发布。需单独登记发布 generation、处理原 key 前缀冲突和中断窗口、保留旧暂存，才可配置恢复实例使用新根。发布和应用读取验证尚未实现；本轮不直接改部署配置或启动实例。
