# 备份只读认证与候选计划协议

本协议在实现前确定，仅增加内部 CLI，不修改冻结的 API、DDL 或归档协议。

`backup inspect --input-root ABS --request-key KEY [--format json|text]` 通过现有安全文件驱动读取私有目录。请求严格 JSON：`request_format=ylreader-package-inspection-request`、`request_version=1`、`package_key`、`identity_key`、`backup_id`、`ciphertext_bytes`、`package_sha256`、`available_header_kids`、`limits`。路径为目录内相对 key，禁止穿越。请求最多 16KiB，单个 X25519 identity 文本最多 4096 字节。

CLI 限额上限：密文 256MiB、正文合计 512MiB、manifest 4MiB、对象数 10000、单对象 64MiB；这是防误用的保守上限，非容量验收结论。超出拒绝，不静默调整。无环境变量回退。

认证直接使用安全打开文件的 ReaderAt：核对完整密文长度和 SHA256，认证全部 age 数据块，复用严格 USTAR/manifest/成员大小、摘要、顺序及 footer 校验，最后再次核对密文。成员只流入摘要和丢弃器；不创建临时目录或明文文件。并发修改可能导致拒绝；不提供跨外部写者的文件系统快照保证。

摘要只输出备份/来源 UUID、摘要、schema/PG 主版本、数据库字节数、对象数、总字节数。age 认证不证明作者可信；PGDMP 魔数不证明数据库可恢复；声明可用的 header KID 不证明真实密钥可解密。因此这些结论及执行/部署授权始终 false。不输出正文、对象 key、identity、DSN 或原始 manifest。

`backup plan` 使用相同参数，另须 `--config-key KEY`。先真实认证归档，再加载已有冻结配置；要求来源 UUID、限额及 identity 路径匹配。复用 PreparePlan 检查已存在且隔离的存储位置，以及显式本机隔离 PostgreSQL 的只读空库/角色条件。候选计划仅输出 stdout，不保存、不登记、不执行，不创建目录、库、用户或表。计划仍须后续受控执行器重新校验；预览不生成恢复执行能力。

两个命令上下文最多 60 秒。退出码 0 仅表示读取/预览成功；2 参数错误，3 安全打开不可用，4 认证或预览未通过，5 关闭/输出失败。错误固定文本，不回显敏感输入。Windows 不支持安全驱动时拒绝，绝不回退 os.Open。
