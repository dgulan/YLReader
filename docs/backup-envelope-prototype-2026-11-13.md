# 加密封装与契约结构原型（2026-11-13）

范围：独立测试模块 scripts/prototypes/backup-age，以及备份 manifest/config 的离线结构校验。未修改后端模块、冻结契约或数据库；未生成业务备份、部署或合并 PR。

## 固定依赖与验证结果

采用官方 [age v1.3.2](https://github.com/FiloSottile/age/releases/tag/v1.3.2) 的 X25519 模式。Go 模块的 go.mod/go.sum 单独提交候选，CLI 由同一锁定依赖图编译，使用 Go 1.26.11，不下载运行浮动 latest。

- 原型自有测试：3 个顶层测试、30 个含子测试通过事件，0 失败/跳过。
- 官方 age 根包：24 个顶层测试通过、616 个含子测试通过事件、244 个子测试 skip，0 失败。包含 TestVectors/CCTV。跳过项包含“不合法头不能再 round-trip”“armor 不实现 ReaderAt”等上游限定，不把 skip 算作通过。
- 契约样本：35 项检查全部通过，覆盖正向示例和负向拒绝。
- 最终官方 CLI 的 govulncheck v1.9.0 二进制扫描覆盖 7 个模块及 Go 标准库，报告 No vulnerabilities found；这是本轮数据库和构建条件下的结果，不是未来安全保证。

原始报告在忽略目录 .toolchain：backup-age-tests.jsonl、backup-age-upstream-tests.jsonl、backup-contract-checks.json、backup-age-vuln-cli-final.txt。构建产物 backup-age.exe 仅为测试工具。

## 实际覆盖

库加密→官方 CLI 解密、官方 CLI 加密→库解密，分别覆盖 0、1、65535、65536、65537、131089 字节。使用随机合成 X25519 identity 和二进制明文，包含空内容及 64 KiB 加密块边界。

错误密钥、头部篡改、中间块篡改、最终 tag 篡改、头截断、尾截断、缺最后块及额外尾数据均被拒绝。同内容重复加密产生不同密文。解密必须读取到完整认证 EOF，不把成功解析 header 当作成功包。

USTAR 样本包含 manifest.json、database.dump 占位数据及一个含中文/emoji/NUL 的正文对象；age 往返后逐项验证名称、顺序、格式及原始字节一致。CLI 与库来自相同实现；不能将这个结果宣称为独立实现之间的互操作。官方 CCTV 提供额外公开向量覆盖。

## 下载及扫描过程中的发现

直接网络下载超时后经现有本机代理取得模块，保留 Go sum 校验。首次源码扫描仅扫描测试模块不足以说明 CLI 完整依赖安全；因此补充含测试路径扫描与最终 CLI 二进制扫描。

最初 age 默认依赖 x/crypto v0.55.0：扫描显示本次调用符号无可达漏洞，但包/模块层仍有 GO-2026-6355、GO-2026-6354 和 GO-2026-5932。未以“不调用”忽略；独立模块升级到 x/crypto v0.59.0、x/sys v0.50.0，依赖图选择 x/term v0.48.0，重新构建、执行全部上述测试并扫描，最终没有发现。初始详细报告 backup-age-vuln-cli-before-verbose.txt 保留。

曾有未包含 CLI 工具依赖的 tidy 导致 readonly 构建缺校验条目，现用 Go tool 指令登记官方 CLI，并固定完整依赖；没有关闭 readonly/sum 校验来绕过问题。

## Schema 及语义边界

docs/backup-contract.schema.json 使用 JSON Schema Draft 2020-12。正向示例仅为合成结构，不含真实凭据，digest 占位不构成有效归档。Python jsonschema 4.26.0 及其依赖固定于 schema-requirements.txt，安装至忽略目录，未修改系统环境。

35 项检查包括：未知字段/格式、错误迁移校验和、UUID/日期、时序、大小汇总、重复 key/ID、路径穿越、NUL/反斜杠/盘符、对象上限、禁止内嵌密码、相同实例、相同/嵌套/Windows 大小写别名存储区、暂存区重叠、缺加密文件引用、重复 JSON 键及非有限数字。

字符串路径比较无法证明实际路径隔离：符号链接、挂载别名、网络存储身份与 ACL 必须由后续驱动解析并验证。schema 校验也不证明 key 文件存在、DSN 安全、数据摘要正确或恢复可用。

## 后续门禁

加密和数据库两类原型分别通过，不等于数据库+文件+密钥的端到端恢复闭环。仍须明确 LocalFS 明文暂存权限及别名处理、包与计划/状态的完整结构、实际容量预算和租户数据验收，再更新正式冻结记录。当前仍为 Draft，无新增业务 API/恢复命令。
