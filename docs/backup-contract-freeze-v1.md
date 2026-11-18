# LocalFS 隔离备份恢复设计 v1 冻结记录

日期：2026-11-13。状态：Frozen design；对应 ADR-019 / Frozen v1.5 修订 3。只冻结隔离实验 CLI 的设计，不是业务实现、全产品验收、生产迁移、合并或部署批准。

## 范围与决策

- 固定 manifest、LocalFS 实验配置、恢复计划及持久操作状态，四类结构以 backup-contract.schema.json 为准；示例只含合成值。
- 固定 age v1/X25519 协议及独立原型依赖，可信 schema 基线与有序数据恢复；不执行归档 DDL、不支持不可信包。
- 固定新空目标、回环隔离数据库、文件 owner/ACL 与链接拒绝、物理身份绑定、计划有效期及取消/崩溃规则。
- 容量数字是保护上限，不是实测吞吐承诺。真实空间检查、对象限额和平台能力不满足时拒绝。
- 仍为隔离实验目标；正式业务 CLI、生产入口、S3/跨驱动、原版迁移及发布不在本设计 v1 的实现验收范围，不取消项目宪章中的完整交付要求。

## 复核依据

数据库 20 项检查通过，见 backup-db-prototype-2026-11-13.md。加密原型 3 个顶层/30 个含子测试通过事件，官方 age 24 个顶层通过，官方子测试 skip 单独记录；最终 CLI 漏洞扫描无发现，见 backup-envelope-prototype-2026-11-13.md。

本轮 Windows Core 12 项、Linux 非 root 9 项、结构/计划/状态纯模型 65 项检查通过，见 backup-localfs-prototype-2026-11-13.md。PowerShell 5.1 未运行成功，不计通过；并发 CAS、句柄竞态、额外 POSIX ACL、跨平台持久性及完整对象/密钥/数据库恢复尚须实现验收，不能由模型检查替代。

对照 AGENTS 复核：未读取参考实现、未引入旧代码；契约先于业务实现。仅 architecture 登记设计修订，OpenAPI 43 操作、SQL DDL、数据库版本及已发布 baseline 字节不变；无需业务数据库迁移。

## 冻结文件 SHA-256

以下摘要基于 UTF-8/LF；修改这些协议文件必须登记修订原因并重新校验模型/样例。此记录本身不计算自引用摘要。

| 文件 | SHA-256 |
|---|---|
| docs/backup-restore-design.md | 42eff4826660ac24b147fdeb1e3524be26f8f9127162458e7fa4e77c78e5d0a6 |
| docs/backup-contract.schema.json | 9b61fb4748775fbeae1d438ebe6294701dda258a6b4b7ce0fa175f16d8a6a2cb |
| docs/examples/backup-manifest.json | 63440ea85aa01bf78bdfc5884b79a3fa16732f0810988c661b36729be5608a63 |
| docs/examples/backup-config.json | ee6aece0ad1f821cfc18a952dcf394e9a46b4513041042c27c7e9a1882860ee2 |
| docs/examples/restore-plan.json | ccda58f154788ec02cc51c68f02e36b4adb288e988a8046879fc7e8a17387fba |
| docs/examples/restore-operation.json | dec3f19b593a2082edcc3b1c2115884e3d84b1c00eecb106e7202f354af55ed6 |

## 实现前后要求

下一阶段按本冻结设计实现独立工具；每个子模块必须运行相应单元测试。首先交付受限目标检查与安全暂存/控制区，再完成封装和恢复编排，在隔离数据集进行端到端及故障验收。未通过这些验证时不得提供长期数据安全承诺，也不得打开生产写目标。
