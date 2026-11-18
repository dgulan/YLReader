# 只读状态诊断与独立命令验收

日期：2026-11-16。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；沿用未提交工作树，未部署、提交、推送、连接业务实例或切换服务。

## 实现

按 [先行协议](backup-status-cli-design-2026-11-16.md) 新增 backup/status.go 与 backend/cmd/backup。只读 API 仅接受 Get，命令仅使用 Get/Close；两轮读取三份控制记录及两份可缺省报告/登记的原字节摘要，核对登记前后版本链，不创建文件、租约或任何恢复状态。

区分 absent / prepared-base / prepared-next / registered-next、漂移、过期、部分声明、损坏/缺失及读取变化。registered 只表示可见，持久确认固定 not_inferred、部署/切换标记固定 false；与确认丢失后落盘的同样内容不会被推断为恢复成功。没有数据库/Redis 探测、凭据读取或恢复执行路径。

命令 `backup status --control-root ... --operation ... [--format json|text]` 严格拒绝未知/重复参数及非规范路径/UUID，无环境回退。安全驱动不支持的平台失败关闭；Windows不使用普通 os.Open 降级。返回码0只是可见且自洽，需核对状态返回4，能力不可用返回3，参数错误2，输出/关闭失败5。详见 [使用说明](backup-status-cli-2026-11-16.md)。

## 结果

- Windows Go1.26.11：backup 包 **76 个顶层/356 个含子测试通过事件**；cmd/backup **4 个顶层/6 个含子测试通过事件**，均0失败/跳过。日志 `.toolchain/backup-status-windows-20261116.jsonl`。本轮新增诊断4个顶层/20个含子测试事件及命令4个顶层/6个事件。
- 覆盖成功/确认丢失可见记录、prepared两种版本、过期/部分/漂移、登记无报告、绑定错误、取消/未来时间、畸形及两轮变化、权限错误不冒充缺省、脱敏、命令格式/返回码、Windows能力拒绝。
- `go vet -tags=integration ./internal/backup ./cmd/backup`、gofmt、Python py_compile、git diff --check（core.safecrlf=false）通过。
- 冻结契约65项检查、16个摘要一致。公开 API、DDL、迁移基线、冻结备份/report/operation格式未变。
- 审计打包分类已将 cmd/backup 纳入备份模块；本轮未重新生成或上传审计包。

初次独立 CLI 构建在当前沙箱 VCS 自动标记处失败（Git退出128）；改用显式 `-buildvcs=false` 构建成功，未修改 Git全局/仓库信任配置。单测与静态检查此前已通过；未将初次构建失败计为通过。

## Linux 独立进程验收

目录 `.toolchain/backup-status-cli-d9a029ded845/`，result.json passed=true、cleanup=true。**1个顶层/7个子测试，共8个通过事件，0失败/跳过**。

- JSON、文本、prepared-next注意状态、过期报告历史、链接目录拒绝、不安全权限拒绝、原控制树文件内容/目录项/模式不变。
- 使用安全文件驱动准备私有tmpfs合成记录和实际CAS登记，再调用真实命令子进程。不是只调用解析函数，也没有数据库或Redis服务；缓存redis镜像仅作为静态Go二进制的Linux运行环境。
- network=none、无宿主端口、UID10001、只读根、cap-drop=ALL、no-new-privileges；仅挂载只读二进制/源码/文档，tmpfs为/tmp与/data。无OOM或重启，唯一临时容器按本轮标签清理。
- CLI SHA-256：`aac8d381c00b23f5f51d662fb7229366290fb50a74a80df2cc7202a2ed458a3a`；测试二进制SHA-256：`9497b64ded544740f3e9a74585bae57a6bab5f9bc933e33c1254f4148a915b9a`。

fixture 故意改变登记状态/权限后由测试恢复原值；命令不执行这些写入。验证比较内容、目录项与模式，不宣称访问时间等文件系统读元数据逐位不变。报告时效按命令诊断基准时间观察，不提供执行时效或后续一致性保证。

## 复现与剩余工作

backend 目录使用仓库 Go1.26.11/GOPATH/GOCACHE/GOTMPDIR，设置 GOOS=linux、GOARCH=amd64、CGO_ENABLED=0：

```text
go test -mod=readonly -tags=integration -c -o ../.toolchain/backup-status-cli-linux.test ./internal/backup
go build -buildvcs=false -mod=readonly -o ../.toolchain/backup-status-linux ./cmd/backup
```

项目根运行 `python scripts/verify-backup-status-cli.py`。脚本不接收业务路径或DSN，只创建本轮私有合成控制区，容器结束清理。

本轮没有重跑真实归档/HTTP恢复全链、Linux race、完整后端/前端回归或真机验证，未将此前52个恢复事件计为本轮新通过。未验证ARM/NAS/Windows安全驱动或断电持久性。

第1工作包已有首个只读status入口，但备份inspect、恢复plan命令仍未接入；通用真实用户授权运行器、受控执行CLI及实际目标交接也未完成。下一步完善只读备份/计划预览，再连接通用受控入口，不因本命令返回0而放行生产恢复。
