# 只读备份认证及候选计划验收

## 结论及边界

已完成 [内部协议](backup-preview-cli-design-2026-11-16.md) 与 [使用说明](backup-preview-cli-2026-11-16.md) 对应的 inspect/plan CLI。status 保留原有行为。归档认证复用既有严格校验器，但用流式丢弃消费者代替写入；新类型不暴露明文、恢复守卫或执行入口。源文件双摘要检查可发现本轮读取中变化，不声称拥有外部写者的原子快照。

此次 CLI 集成使用真实 age 加密的合成归档，其 database.dump 只有合成 PGDMP 内容；另对真实隔离 PostgreSQL16 执行空库只读检查。**没有执行此次归档的 pg_restore，不能把本轮结果当作真实数据库恢复成功。** 既有真实 pg_dump/恢复链证据仍见先前报告，本轮没有重跑该完整链。作者可信、数据库语义验证、header 解密、执行/部署授权仍为 false。

## 验证结果

- Windows Go1.26.11：backup 80 个顶层、376 个通过事件；cmd/backup 5 个顶层、7 个通过事件；无失败/跳过。日志 `.toolchain/backup-preview-unit.jsonl`。覆盖错误密钥、密文损坏/截短/追加、摘要不符、缺 header KID、成员摘要错误、重复条目、取消、源文件变化、严格请求/限额、关闭失败和固定错误输出等。
- Linux amd64 独立 CLI：1 个顶层 + 9 个子测试 = 10 个通过事件，无失败/跳过。检查 JSON/text 认证摘要、候选计划、错误 identity、损坏包、来源不符、非空目标、符号链接及不安全权限。命令后文件内容/权限一致；候选计划后目标仍为空。测试对照夹具显式创建并删除的障碍表不属于 CLI 写入。
- go vet（integration 标签）、gofmt、Python 编译与 git diff --check 通过。65 项契约检查、16 个冻结文件摘要匹配；未改公开 API/DDL/冻结归档协议。

最终运行目录 `.toolchain/backup-preview-cli-0590069d92d0/` 的 result.json 为 passed=true，两个自有容器 cleanup 均 true。此前一次同范围运行 `.toolchain/backup-preview-cli-622637326f47/` 也通过；最终源文件空上下文保护及漂移单测补齐后重新构建并复核。

| 最终产物 | SHA256 |
|---|---|
| backup-preview-linux | 6189a6ab7015f48eb699d565ef298531d240bb54c99caa4744812d1038557cb9 |
| backup-preview-cli-linux.test | d26a65806b3a00c432d74585e9c82a9ecc8a0ddd377a0c42d276d1f7f83c33b4 |
| PostgreSQL 镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| CLI Linux 运行镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

脚本 scripts/verify-backup-preview-cli.py 使用缓存镜像，不发布宿主端口；PostgreSQL network=none，测试容器仅共享其隔离网络。CLI UID10001、只读根文件系统、cap-drop ALL、no-new-privileges，源码/文档/二进制只读挂载，数据仅自有 tmpfs；有 CPU/内存/PID 限额。测试结束删除本轮标签匹配的容器及临时卷，不触及业务实例。

## 后续

第1工作包的 status/inspect/plan 只读入口已齐备，尚无通用授权/受控执行、生产恢复或服务交接。下一步先冻结真实用户授权/凭据及中断处理契约，再接入已有恢复守卫；不能从候选计划或文件报告重建执行许可。

Windows 安全文件驱动、原生 ARM、真实 NAS、iPhone Safari 及真实书源阅读仍保持原待验状态。本轮没有部署、提交、推送、合并、业务库演练或生成新审计 ZIP。分支 codex/pre-release-20261113，HEAD fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4；既有未提交工作保留。
