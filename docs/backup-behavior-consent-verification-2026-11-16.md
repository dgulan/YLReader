# 通用行为验收输入预检验收

## 实现范围

依据先行 [输入及中断契约](backup-behavior-consent-design-2026-11-16.md)，新增 backend/internal/backup/behavior_consent.go。只读加载操作员意向和独立凭据文件，将操作/owner/计划/目标/generation/三记录摘要/预期运行修订/有效期绑定；限制1..8个账号、文件大小、重复账号/key及路径穿越，前后重读拒绝变化。口令保持原字节，不 trim；返回类型无公开字段/执行桥接，JSON 序列化失败，常见 fmt 格式对指针、值及单账号均脱敏。

这是内部输入预检，不是账号认证、真实用户同意证明、完整 JWT/refresh 验证、目标配置实测或恢复执行许可。未添加 CLI/API、HTTP 请求、数据库读写、动作账本或报告 pass；没有放宽现有 isolated-http-fixture-v1 证据门禁。运行修订摘要必须由后续可信装配器提供，文本匹配不能证明配置已运行。

Close 要求调用者独占、不可与访问或复制并发；丢弃引用并清除原始读取缓冲，不承诺 Go 字符串及 GC 副本安全擦除。输入可以重复预检，尚无持久防重放。后续首次登录前必须持久登记动作意向，取消/响应丢失不能自动重试。

## 验证

- Windows Go1.26.11：新增3个顶层/39个子测试（42个通过事件），覆盖绑定、窗口、重复/未知/null JSON、账号形状、超限、输入/记录漂移、读取中失效、取消、存储及关闭错误、脱敏及序列化拒绝。
- backup 全包83个顶层/418个通过事件；cmd/backup 5个顶层/7个通过事件，无失败/跳过。日志 `.toolchain/backup-consent-unit.jsonl`。既有合成行为证明和只读 CLI 单测同时回归；未重跑完整真实 pg_dump/恢复/HTTP 链。
- Linux amd64 安全驱动：1个顶层/3个子测试（4个通过事件），实际私有文件读取、0644凭据拒绝、符号链接拒绝。前后对比文件内容/权限未变；反例由夹具显式改变/恢复文件，不属于预检写入。
- go vet（integration标签）、gofmt、Python编译、git diff --check通过；65项契约检查和16个冻结文件摘要匹配。未改公开API、DDL或冻结备份协议。

运行 scripts/verify-backup-behavior-consent.py，结果 `.toolchain/backup-consent-40be5c5c90b5/result.json` 为 passed=true、cleanup=true。测试二进制 `.toolchain/backup-consent-linux.test` SHA256：`6b491c8dbaa998569e48a42821ad7225b25473769e879b102b4c3d647f2d9d7f`。缓存 Linux 运行镜像 SHA256：`c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b`。

唯一自有容器 network=none、UID10001、只读根、cap-drop ALL、no-new-privileges，源码/文档/二进制只读挂载，测试文件仅tmpfs；没有启动 PostgreSQL、Redis服务、应用或对外端口。execution_authorized=false；容器已清理。

## 后续

先设计/实现首次登录前的持久动作意向、一次性执行边界及响应丢失诊断，再在 live guard 下核验真实账号/旧凭据并接入受限行为运行器。尚未接通真实用户报告或受控恢复执行，不据此推进ready_for_cutover/completed。

本轮未部署、提交、推送、合并或接触业务库。分支codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4。已有Gemini审计包保持此前快照，未重新生成；本轮新文件不在旧包中。
