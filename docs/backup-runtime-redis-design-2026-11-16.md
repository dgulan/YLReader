# 交接运行配置与 Redis 核验内部协议 v1

日期：2026-11-16。先于实现确定本轮边界，不改变公开 API、DDL、既有冻结备份协议或恢复状态。

## 运行配置装配

调用方显式提供预登记与实际装配的 target_instance_id、config_revision_id、jwt_revision_id、keyring_revision_id（规范 UUID）及 current_header_kid，两份元数据必须完全相同。直接使用显式传入的 source/target JWT 字节和已经安全装载的目标 Keyring；不读取进程环境、业务文件，不修改全局 keyring。

源、目标 JWT 均至少 32 字节且不超过 4096 字节，必须不同；仅在内存比较，不计算秘密摘要。检查当前 KID 与装配声明一致、全部有序且无重复的 required KID 可用。随后对当前 KID 作临时随机挑战的 Seal/Open 自检，不输出挑战或密文。该自检不能替代恢复 header 的逐条认证，也不能证明 JWT 熵、运行服务确实加载此配置或配置修订标识可信。其余 KID 的正确性沿用既有全量 header 认证门禁。

全部成功返回 SHA-256(固定顺序 JSON：format=ylreader-runtime-config-revision、version=1、上述元数据及 required_header_kids)。元数据不得含凭据/路径；配置变化必须由装配者更新修订 UUID。相同修订标识不能检测秘密字节被替换，这一约束不可包装成已完成的运行服务实测。

## Redis 只读观察

源、目标必须为显式 redis:// 回环字面 IP、显式端口和规范 /0..15 数据库索引；拒绝 DNS、查询参数、fragment、legacy 地址和环境回退。本轮不扩展到 TLS/代理/集群，不放宽成任意网络目标。允许 URL 显式 ACL 凭据但不输出。调用方独立提供预登记 run_id（40 位小写 hex）和 database，目标与源必须不同 run_id；同进程不同逻辑库也拒绝。

隔离见证由调用方在已获授权的自有目标预先创建：同一 operation UUID 对应唯一 key `ylreader:handover:<UUID>`，两端各保存不同的 32..64 字节随机值并设置短 TTL。探测器只读取，不创建/删除/刷新 TTL。缺失/过期、错误值、相同值或权限不足全部拒绝；未知来源的“见证值”不构成独立隔离证明，预配置仍是信任边界。

缓存客户端新增专用观察方法：同一连接 INFO server → INFO replication → GET 唯一见证 key → INFO server → INFO replication，前后 run_id 必须相同；只接受 standalone、master、connected_slaves=0，拒绝集群、副本及存在已连接副本的主节点。输出仅 run_id、配置的 database 和见证值给内部调用方。INFO 最多 64KiB、见证最多 64 字节，严格校验 RESP 长度及 CRLF、拒绝重复/缺失/非法 run_id 或角色字段，错误统一脱敏。保留既有 AUTH/SELECT；取消关闭观察连接，不新增 FLUSH/KEYS/EVAL/DEL/写命令。此时点的角色检查不能阻止后续新增复制关系。

交接探测器依次读取 source、target、source、target，每次比对预登记身份和各自见证，捕获跨次变化；全部通过返回目标 run_id + database 的固定格式 SHA-256，绝不输出见证、URL/密码或原始 INFO。INFO 中 run_id 是 Redis 随机运行标识，见 [官方文档](https://redis.io/docs/latest/commands/info/)。不是持久身份、原子快照、租约或后续路由稳定保证；物理克隆、代理伪造及非协作变更仍需外部隔离。

## 验证与后续

单测真实密钥装配、缺失 KID、同 JWT、元数据漂移和脱敏；缓存协议测试长度上限、畸形/权限错误及 run_id 变化。真实两个独立 Redis7 容器无外网、无宿主端口、tmpfs，见证由夹具预置；验证成功、别名、身份漂移、错误见证、缺失、ACL 拒绝及取消。探测期间原见证和哨兵保持不变，无清空、删除或配置写入。

本轮只交付两个探测基础，不生成全通过交接报告，不推进 ready_for_cutover/completed，不部署或启动业务实例。真实安全配置文件的端到端装载、源/目标身份预登记、报告编排及持久 CAS 另行完成。
