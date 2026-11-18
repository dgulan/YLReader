# 原 key 对象发布内部协议 v1（2026-11-14）

实现前确定以下隔离 LocalFS 发布边界，不改公开冻结结构或部署配置。

- 新发布仅接受同一 restoreGuard 中刚完成 header 全量检查、数据库明确 committed 且结果登记成功的对象；发布前再次只读核对真实数据库、header、对象引用及会话/任务安全状态。状态重放不产生解密证明，不能自动 ready_for_cutover/completed。
- 保留控制/目标目录租约、配置路径及源暂存句柄。写入前重验计划、目标身份及唯一暂存 generation；不接受额外目标对象。
- 原 key 全部通过安全驱动规则：4096 字节、64 层、每层 255 字节，拒绝重复、文件/目录前缀冲突、链接及非法 key。受限 key 在发布写入前整体拒绝，不静默改名。
- 独立控制记录 operations/<id>/publication.json：publication_format=ylreader-object-publication、publication_version=1、operation_id、backup_id、plan_digest、owner_id、package_sha256、manifest_sha256、target_storage_fingerprint、staging_reference、pending_reference、output_reference、tree_fingerprint、state。全部必填。pending_reference=.publish-<去连字符操作UUID>，output_reference=objects-<操作UUID>；tree_fingerprint 初始为空，随后一次设置为目录身份摘要。
- 状态只允许 planned→copying→publishing→verified。planned 先于树创建，copying（含实际树身份）先于对象字节，publishing 先于原子目录改名；verified 只在同步、改名及最终回读成功后发布。重复请求只读已有记录，非 verified 返回待核对，不重放。
- 驱动排他新建私有 pending 目录，通过目录 FD 不可覆盖写原 key，逐对象复制/回读摘要，文件与目录同步。树内没有元数据文件，避免与原 key 冲突。
- 验证后用同卷 renameat2 RENAME_NOREPLACE 改为最终名称并同步父目录；不能覆盖已有最终目录。不能只凭目录可见判定成功。
- Close 只关闭树及父目录 FD，不删除数据。所有失败保留 pending/最终树及原平面暂存；不递归删除配置路径，不自动续跑。目录创建到身份登记间崩溃可能留下空树，须核对。
- 发布新对象根不等于启动服务；不使用可覆盖的普通 LocalFS.Put 实现发布，仅在隔离测试中验证现有 Repository/Provider 可读，不改应用环境或现有卷。
