# 包认证、manifest 与 tar 校验实现验证（2026-11-13）

依据 ADR-019 继续实现包验证层。新增 manifest.go、manifest_reader.go、package.go 及相应测试；没有注册 API、提供业务恢复命令、运行 SQL 或部署。本轮只读取合成样本，不连接数据库。

## 已实现的验证链

1. 校验包描述符的 backup_id、密文字节数/SHA-256 和策略预算；检查暂存文件系统可用字节，并在本轮私有暂存区排他保存密文。源流错误不回传可能含秘密的底层 message。
2. 使用已固定的官方 age v1.3.2，只接受单一 X25519 recipient。ReaderAt 解密先读取全部明文到丢弃端，逐块完成认证，然后才写入私有 authenticated.tar 并允许解析。只验证头或最后一个加密块不足以返回成功。
3. manifest 原始字节先做 UTF-8 和 SHA-256 检查，再逐项解码对象清单，校验冻结字段、时间、迁移、14 张表计数结构、对象 ID/key 唯一、排序、大小汇总和 header kid 覆盖。计数结构校验不代表已检查数据库内部的实际行数。
4. 检查每个物理 USTAR header，而不让高层 tar.Reader 静默吞掉扩展条目。只接受 manifest.json、database.dump、按 opaque-id 排序的登记对象；拒绝链接、PAX/GNU 元数据、额外/缺失/重复或错序条目、非零 padding、截断以及非零尾随内容。要求完整双零结束块，零填充尾部最多 10 KiB。
5. 逐文件流式校验字节数和 SHA-256。对象原 key 只作为元数据，私有解包名称为受控 opaque-id，不把原 key 拼接为宿主路径。database.dump 目前检查 PGDMP 魔数与完整字节摘要，不调用 pg_restore 或执行其中内容。
6. 成功才返回拥有私有暂存区的 VerifiedPackage 和 PackageBinding；有活动读取句柄时 Close 返回 Busy。验证失败自动清理，清理失败返回稳定 CleanupFailure 和不含原始用户文件名的暂存引用，不能报告成功。

认证确认的是接收方可解密及内容完整性，不是生产者签名。包仍必须来自受信源；不能把“能用自己的 age 私钥解密”当作“归档中的 SQL 安全”。

## 资源与解析边界

密文、明文、manifest、对象数/单对象及剩余空间均检查预算。暂存占用包括密文、完整 tar 和解出的 payload，空间检查不构成配额预留；中途写入失败仍必须处理和清理。

正文保持流式读取；manifest 对象逐项解码，排序和唯一性索引为 O(对象数) 元数据，不是常量内存。已验证一个超过 1 MiB、8000 个对象的有效清单，未冒充 100 万对象容量或 256 MiB 清单的性能验收。

在 json.Decoder 分配 RawMessage 前增加词法预算：字符串与单条记录 16 KiB、普通 header 容器 1 MiB、连续空白 16 KiB、标量 128 字节、嵌套深度 8；顶层 objects 数组可以跨越整个配置允许的 manifest。超额归为资源拒绝，不尝试分配巨大字段后才检查。规范外的大量冗余空白可能被拒绝；策略上限不是保证任意格式的数据都能在任意机器处理到上限。

共用 JSON 加载器另拒绝未配对 UTF-16 转义，避免 encoding/json 将其静默替换为 U+FFFD 而改变 key/文件引用；合法代理对、转义反斜杠及真实 U+FFFD 保留。

## 验证结果

- Windows 后端普通全包回归：98 个顶层通过、219 个含子测试通过事件，0 失败；4 个既有辅助/未配置数据库测试跳过，不计通过。日志 .toolchain/backup-package-windows-unit-final.jsonl。
- Windows 目标包 vet 及 Go 模块校验通过。
- Linux 两个目标包 race：39 个顶层通过、80 个含子测试通过事件，0 失败/跳过/数据竞争报告。日志 .toolchain/backup-package-linux-race-final.jsonl。非 root、无网络、源码/docs/模块缓存只读；没有启动数据库。
- Linux 真实私有暂存验证了成功读取、活动 reader 清理保护、最终清空、摘要失败后的无残留；不是仅靠内存 fake 认定文件驱动通过。
- 控制区竞态修复的三项测试各重复 10 次，30 个通过事件，0 失败/竞争报告；日志 .toolchain/backup-package-cas-repeat.jsonl。
- 新增依赖和包调用链 govulncheck 无发现；日志 .toolchain/backup-package-vuln.txt。后端固定 age v1.3.2、hpke v0.4.0，继续使用既有修复版 x/crypto v0.59.0 与 x/sys v0.50.0；不更换为浮动版本。
- 65 项冻结契约模型检查及 16 份冻结摘要匹配；OpenAPI、DDL 和历史迁移保持不变。

## 首轮 race 发现与修复

首次 Linux race 在 TestControlWithDurableLocalFiles 暴露旧问题：读取方刚打开旧 inode，CAS 原子替换后它的 nlink 可变为 0，旧文件权限验证将其误判为 UnsafeStorage。该轮失败证据保存在 .toolchain/backup-package-linux-race.jsonl，没有算作通过。

现在对已打开但被替换/删除的 inode 返回 Conflict，调用方刷新重试；不会把 nlink=0 的文件作为通过验证的数据返回。nlink>1 的持久硬链接仍拒绝。新增确定性“打开后删除”回归，并通过重复并发 race；没有放宽读权限或硬链接规则。

## 下一步与未交付范围

本层验证 age、manifest 与外层 tar/对象字节，不验证 PostgreSQL custom archive 的完整目录、数据行语义、可信基线表/列匹配或实际 header 解密。单位样本的 database.dump 明确为 PGDMP 开头的合成字节，不宣称是可导入的真实数据库备份。

下一步实现受限 pg_restore 归档目录检查与对象清单核对，再接入仅针对新隔离目标的恢复编排。生产目标、S3、Windows 原生安全文件驱动、完整数据库/对象/密钥恢复和发布仍未交付；不能仅凭 VerifiedPackage 对象启动生产恢复。

本轮未提交、推送、合并、部署或操作业务数据库。
