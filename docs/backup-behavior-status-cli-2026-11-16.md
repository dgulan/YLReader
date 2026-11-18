# 动作账本只读命令及操作员核对

Linux非root、既有私有控制区可使用：

```text
./backup-behavior-status-linux behavior-status --control-root /absolute/private/control --operation 40000000-0000-4000-8000-000000000001
./backup-behavior-status-linux behavior-status --control-root /absolute/private/control --operation 40000000-0000-4000-8000-000000000001 --format text
```

路径/UUID为占位值，需替换为该次操作原始控制区和operation_id。控制区下应有operations/<UUID>/记录；单独导出的验收报告、日志、Gemini包或压缩备份不是控制区。已清理的tmpfs验收夹具不能再用报告目录代替查询。工具不创建目录或修复权限。

目录0700/文件0600、当前用户拥有、无不安全链接，沿用安全驱动校验。Windows原生安全驱动未支持，返回3，不回退普通读取。ARM/NAS未由本轮验证。命令不需要用户名/密码、JWT/age/header密钥、数据库或Redis地址，禁止添加真实凭据参数。

## 如何解释

| 状态 | 含义 | 操作员下一步 |
|---|---|---|
| responses_observed | 全部账号的响应记录链完整，存在登出响应声明 | 如需判断实际撤销，另做显式目标身份绑定的只读数据库复核；此处不证明撤销或历史写确认 |
| outcome_unknown | 有效但不完整或响应未知；可包含父response未确认的孤立logout | 停止再次登录，保留所有记录；不能猜测或撤销未确认会话 |
| intent_not_observed | 此次未见意向且无孤立动作 | 核对operation与原控制区；不据此认定过去未执行 |
| stale | 窗口过期、版本已变或操作状态不再匹配 | 核对当前操作及时间，不手改时间戳/generation或覆盖旧记录 |
| inconsistent | 账号/操作/父摘要/顺序/会话等关联矛盾 | 核对是否混用了不同操作/快照，保留原文件和错误结论 |
| changed | 两轮读取不同 | 先确认写入者状态，再只读观察；重复读取不是执行重试 |
| unavailable | 缺控制记录、权限/关闭/格式/超限等问题 | 核对控制区来源及读取能力，不将读取失败解释为记录不存在 |

JSON只含聚合计数，不显示用户/session清单、正文或凭据。text明确显示“数据库撤销：未核验”。logout_response_claims数值是文件声明，不是数据库查询结果。persistence_confirmation始终not_inferred，dispatch_authorized/retry_authorized/deployment_authorized始终false。

固定读取0..7的24个动作槽，不枚举其他文件；不对整个目录无额外文件作保证。重复读不提供原子快照或ABA防护，输出后状态仍可能变化。

| 退出码 | 含义 |
|---|---|
| 0 | responses_observed，仅表示响应记录完整 |
| 2 | 参数错误，尚未打开目录 |
| 3 | 安全目录或平台能力不可用 |
| 4 | 已输出需人工核对的诊断 |
| 5 | 输出或目录关闭失败；关闭失败不输出成功结果 |

不要将0用作恢复、重新登录或服务切换门禁。4也不是让脚本自动重试的信号。超时/响应丢失后，不删除dispatch/response/logout，不补写父记录，不覆盖意向，不刷新窗口或清空会话表。已知会话与未知会话的清理仍须按后续明确授权的受控流程处理，本命令不执行任何清理。

数据库会话观察当前仅有独立内部接口，不能通过本命令或环境变量触发。已有 [内部受限HTTPS运输层](backup-behavior-http-verification-2026-11-17.md)，但没有生产配置/服务生命周期入口、通用报告或受控恢复CLI，不能把现有合成报告当作真实用户交接。

## 与其他命令配合

先用相同operation运行既有status了解操作/报告/登记版本链，再用behavior-status核对动作响应链。status观察到registered并不推翻behavior-status的未知或矛盾；两者均不推断历史持久确认。inspect认证归档，plan预览隔离目标，也都不授予派发或恢复权限。

构建入口backend/cmd/backup：

```text
go build -buildvcs=false -mod=readonly -o ../.toolchain/backup-behavior-status-linux ./cmd/backup
```

从Windows交叉构建时显式设置GOOS=linux、GOARCH=amd64、CGO_ENABLED=0，使用项目Go工具链和缓存；不表示Windows原生可读取控制区。独立进程验收见 [本轮记录](backup-behavior-status-cli-verification-2026-11-16.md)。
