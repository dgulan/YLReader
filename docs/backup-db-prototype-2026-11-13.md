# 隔离数据库恢复原型验证（2026-11-13）

依据备份 Draft 第 8.4 节，运行可复现工具 `python scripts/verify-backup-db.py`。脚本不接受 DSN、不读取应用数据库环境，只使用本机已有 postgres:16 镜像创建独立临时容器：network=none、无发布端口、无宿主目录挂载、数据目录 tmpfs。仅通过容器内 Unix socket 连接。它是设计实验，不是生产恢复 CLI。

## 环境与结果

- PostgreSQL 16.15（Debian 16.15-1.pgdg13+2）；镜像 ID `sha256:f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657`。复用了本地镜像，没有下载或将该版本认定为新的生产安全基线。
- 最终运行 ID `01437dc0b289`，20 项检查全部通过，退出码 0，cleanup=true。
- 原始报告：`.toolchain/backup-db-01437dc0b289/result.json`；同目录保留归档、顺序清单及恢复 stderr，均为本工具生成的合成数据，未提交。
- 使用当前 docs/schema.sql 建库，14 张表、21 行样本，包含两个用户、本地书目录、映射、Unicode 书签、精确进度、设置、会话和运行中任务。部分表为空，不能据此声称每张表的全部业务分支均已覆盖。
- header 是明确标注的合成不可解密信封，只验证字节保留；未使用真实用户凭据或密钥。

## 验证的行为

1. NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOREPLICATION 的数据库所有者可完整恢复 schema/data；pgcrypto/pg_trgm 可用。它仍有数据库所有者权限，不能把它称为任意不可信 SQL 的沙箱。
2. 源与恢复目标按表逐行 JSON 排序后比较 SHA-256 及行数，全部一致，涵盖 progress 的 version、server_updated_at、char_offset 与 Unicode 字符串。identity 序列下一值高于已有行。
3. 角色不能创建超级用户角色，不能禁用内部 FK 触发器；非法外键写入被拒绝；books 更新后的 version 触发器仍工作。
4. 可信基线建库后，按依赖顺序只恢复 TABLE DATA/SEQUENCE SET，不禁用外键、不提升角色权限，样本字段保持一致。
5. 截断归档被拒绝，single-transaction 失败后新目标不存在应用表。无序 data-only 的失败事务也未留下部分表数据。
6. 在隔离恢复目标撤销会话、增加 token_version、将 queued/running 任务标记 canceled 并清空 worker_id，SQL 状态均符合预期。没有运行应用验证 JWT/refresh 登录行为。
7. 成功、失败及目标后处理之后，源库逐表指纹仍一致。

## 两轮失败证据与设计修订

第一次运行 `0bd1cc78fa5f`：完整恢复通过，但可信基线上的默认顺序 data-only 恢复失败。app_settings 在 users 前加载，违反 app_settings_user_id_fkey。报告 passed=false、cleanup=true；没有禁用外键或把失败计为成功。

第二次运行 `60ab3fd4ca0d`：显式重排了 TABLE DATA，仍从 stdin 读取 custom archive，pg_restore 报告 non-seekable input 无法处理 out-of-order restore。报告 passed=false、cleanup=true。

最终采用完整归档暂存文件，显式重排数据项，使用 `--no-owner --no-acl --exit-on-error --single-transaction` 通过。最终运行仍保留无序外键失败的负向检查，不删除失败断言。契约相应要求：完整认证后才使用可寻址明文暂存，可信基线提供 DDL，数据依赖顺序固定。

所有临时容器均先核对唯一 run label 再删除；三轮报告 cleanup=true。没有更改现有持久实例、应用配置或历史迁移。

## 尚未覆盖

完整 tar.age 封装、官方 age 固定依赖及互操作、明文暂存权限适配、文件对象完整性与读取、真实 header 解密、应用级会话失效、S3 与跨驱动恢复、容量压测均未执行。正式工具还须在同一数据加载事务中处理新隔离目标的基线账本种子，并拒绝未知表、序列、列形状和迁移摘要。

本轮不冻结 ADR-019、不新增 API 或业务恢复实现；下一步完成加密互操作原型及配置/manifest 的机器可校验结构，再汇总冻结设计。
