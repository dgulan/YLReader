# 备份认证与隔离恢复计划预览

Linux 非 root 用户可在自己拥有的既有私有目录中使用：

```text
./backup inspect --input-root /absolute/private/input --request-key request.json
./backup plan --input-root /absolute/private/input --request-key request.json --config-key config.json
```

示例路径是占位值，不是已配置的生产目标。默认 JSON，可追加 `--format text`。目录0700、文件0600，按现有安全驱动检查所有链接、权限和归属。DSN、identity 和配置等秘密文件也必须放在当前非 root 用户拥有的私有0700目录内，不能直接放在通常为1777的 `/tmp` 或0755的 `/etc` 根目录；可由操作员预先建立专用私有子目录，不以放宽权限绕过门禁。Windows 原生安全驱动尚不支持，返回3，不降级读取；未验证 ARM/NAS。不会创建目录或配置、读取环境变量连接信息，也不自动修复权限。

input-root 中由操作员事先放置 age 归档、单个 X25519 identity 文本和请求。请求示意（UUID/SHA 必须换成真实已知值）：

```json
{
  "request_format": "ylreader-package-inspection-request",
  "request_version": 1,
  "package_key": "package.age",
  "identity_key": "identity.txt",
  "backup_id": "30000000-0000-4000-8000-000000000001",
  "ciphertext_bytes": 12345,
  "package_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
  "available_header_kids": ["primary"],
  "limits": {
    "max_ciphertext_bytes": 104857600,
    "max_plaintext_bytes": 209715200,
    "max_manifest_bytes": 1048576,
    "max_objects": 1000,
    "max_object_bytes": 16777216
  }
}
```

KID 列表须升序且无重复，仅声明后续需要的 key 名称；不会读取 header 密钥或验证解密。请求最大16KiB、identity最大4096字节。CLI 上限见 [设计协议](backup-preview-cli-design-2026-11-16.md)，超过拒绝。归档不会解压落盘；摘要不含正文、对象 key 或密钥。age 认证与摘要通过不证明作者可信、数据库语义正确或能够恢复。

plan 额外读取已有冻结格式的 config.json（结构见 docs/examples/backup-config.json）。限额须与请求完全相同，source.instance_id 须与认证 manifest 相同，encryption.identity_file 须为实际 identity 的绝对路径。所有源/目标/备份/控制/临时目录须事先存在且符合隔离条件；显式 DSN 必须指向受限角色拥有的本机隔离空库。plan 进行只读数据库检查，不连接源数据库，不创建数据库、迁移或写入控制记录。

候选计划绑定此次存储身份和归档，期限15分钟；它只在 stdout 输出，并不代表获得执行权。外部并发修改可能使检查失败或使计划随后失效，后续受控执行必须重新检查。退出码0表示只读成功；2参数错误，3安全打开不可用，4认证/预览拒绝，5关闭/输出失败。失败无部分成功输出；错误不回显底层路径或凭据。不要把0作为自动恢复或切换条件。

inspect 无数据库连接；plan 的数据库检查是显式请求的只读预览。两者均未提供恢复、重试、删除、登记、切换或部署入口，execution_authorized/deployment_authorized 始终 false。
