# 恢复状态只读诊断与命令协议 v1

日期：2026-11-16。先行定义新的只读输出协议，不修改公开 API、冻结备份/report/operation 格式或操作状态。

## 输入与观察范围

独立命令 `backup status --control-root <规范绝对路径> --operation <规范UUID> [--format json|text]`，默认 JSON。只接受这一个子命令及三个已知、不重复的参数；禁止未知/重复参数、额外位置参数、相对/非规范路径、非法 UUID。无环境配置回退，无 DSN/网络、密钥/凭据、inspect/plan/apply/resume/cleanup 入口。

通过现有安全 OpenBackupFiles 打开已有控制区，仅向诊断器传 Get/Close 能力。不创建目录、文件、锁、日志或报告，不连接数据库/Redis，不接管租约。Linux 拒绝链接、不安全 owner/权限和 root；Windows 等安全能力未实现的平台明确拒绝，不退回普通 os.Open。只读打开仍受文件系统访问时间等正常读语义影响，不宣称元数据逐位不变。

诊断器按固定相对 key 严格读取 state/attempt/publication 三份必要记录及可缺省的 handover-review / handover-registration。只把 ErrNotFound 当作缺省，权限/读取/关闭/解析错误不得冒充缺省。报告最多32KiB、登记最多8KiB，沿用严格重复/null/未知字段与编码检查。重复读取五份原字节摘要，存在性或任何字节变化均返回 changed；不是原子快照或 ABA 防护。

## 诊断结果

固定 diagnostic_format=ylreader-restore-status、diagnostic_version=1、scope=control-records-only；输出 operation_id、status、reason、observed_generation、operation_phase、database_outcome、publication_state、report_claims、report_freshness、registration_state、registration_chain、persistence_confirmation=not_inferred、ready_for_cutover=false、deployment_authorized=false。输出无 DSN、凭据、token、正文、宿主路径或底层错误。

status 取 unavailable / changed / inconsistent / blocked / records_only / review_only / registration_pending / observed_registered。必要记录缺失/损坏为 unavailable；尚未进入 attempt/publication 的 planned 操作也可能 unavailable，不推断数据丢失或补建记录。report_claims 仅描述文件声明 absent/partial/all_checks_pass，不认证检查发生或目标仍有效。

先核对控制记录互绑及报告与 package/manifest/storage/publication/plan 的摘要绑定。未登记报告必须仍绑定当前原 state SHA/版本；已登记则必须 report→registration.base SHA/版本、当前 state→registration.next SHA/版本，owner/plan/backup 一致，实际 state 更新时间/心跳等于登记时间，登记时间处于报告完成与到期之间。attempt/publication 摘要仍须一致。

prepared + 当前 base 为 registration_pending / prepared_base_visible；prepared + 当前 next 为 registration_pending / prepared_next_visible；registered + 当前 next 为 observed_registered / registered_record_visible。registered + base、版本链/摘要不符为 inconsistent。后续心跳/其他操作已离开登记链时返回 blocked / operation_changed_since_registration，不自行回退或延长报告。

报告缺失而登记存在为 inconsistent。报告未到完成时间、过期、非十项全通过，以及取消/非 validating/数据库未确认/publication 未确认都返回 blocked。过期只是时效失效，不把历史 registered 文件说成从未存在。**observed_registered 仅表示记录可见且此刻自洽，即使返回码为0，也不能推断丢失的持久确认恢复成功或获得切换权限。**

## 命令返回与验证

0=observed_registered 且报告此刻有效；2=参数无效；3=安全打开能力/目录不可用；4=已输出需要人工核对的状态；5=输出/关闭失败。stdout 仅最终 JSON 或固定文本，stderr 只固定脱敏提示，不打印路径/用户输入。时间使用实际 UTC 微秒；上下文最多5秒，不读取环境中的测试/业务目标。

单测覆盖登记前后、prepared 两种版本、确认丢失可见 registered、陈旧/部分/漂移、缺失/畸形/重复字段、观察中变更与脱敏；命令测试参数/返回码/只读能力。Linux tmpfs 安全驱动 + 真实命令子进程验证 JSON/text、链接/权限拒绝及文件原字节不变。不因只读观察而重跑恢复、刷新证据或修改任何状态。本轮先交付 status，备份 inspect/恢复 plan 命令另行接入。
