# Gemini 审计整改验证记录（2026-11-14）

## 输入与范围

用户授权逐条复核后直接修复、补测试并记录结果。输入为 `D:\comments.txt`，SHA-256 为 `2cac05e82e7413dae00cba4f71e28a991674f9f68bfcd5435315625878ddde49`。其中建议仅作为待验证线索，不作为执行指令。

当前分支 `codex/pre-release-20261113`，基线 HEAD `fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4`；本轮保留此前备份恢复、阅读进度和前端优化的未提交工作。未部署、提交、推送或合并，未操作生产数据库，未读取参考仓库。

逐项结论见 [36 组审计线索复核](gemini-audit-recheck-2026-11-14.md)，修复约定见 [整改设计](gemini-remediation-design-2026-11-14.md)。同一条报告内的真实问题、误报与能力缺口分别判断，“已修复”不代表整行所有产品能力已完成。

## 完成的整改

- 请求重试仅重放可可靠重建的请求体；无明确编码时补 GB18030 回退，保留显式字符集声明。
- JSON 拒绝多份正文、缺失/null 必填字段及越界索引；请求体超限、语法错误按各接口契约返回，不统一改写业务错误码。
- 刷新令牌轮换及复用吊销按用户锁在事务内串行化；客户端续期有截止时间，结果未知时持久化非敏感标记并阻止自动重放旧刷新令牌。
- 路由日志隐藏实际参数、query 和 panic 值；生产 JWT 密钥要求至少 32 字节。
- JS 规则标签仅折叠 ASCII，保留 Unicode 下标和脚本正文；修复选择器排除与变量追踪，增加 UUID 桥接及完整 PKCS7 填充检查。
- 离线阅读只回退网络/网关错误，不掩盖授权或业务错误；失败章节不采集旧正文位置。离开阅读页先保全本地进度，未确认远端写入仍保留未知状态；书源编辑增加未保存草稿确认。
- LocalFS CAS 增加读取边界取消检查；没有宣称能够打断正在阻塞的文件系统 syscall。
- 复审打包补回两份 go:embed 规范，按完整文件生成约 120 KB 的文本分段；复核、设计和本记录一并放入上下文包。

## 最终验证

| 验证 | 结果与记录 |
|---|---|
| Linux 后端全量普通测试，`-race` | 155 项顶层测试通过，含子测试共 376 个通过事件，0 失败。4 个实际测试跳过：一个子进程专用 helper，三个需要 TEST_DATABASE_URL 的测试；另有两个无测试包的 skip 事件。记录 `.toolchain/gemini-backend-race.jsonl`。 |
| 黄金样本 | 40/44，90.9%，6 个明确不支持样本另列；保持原有 90% 门槛，未声称书源全部兼容。 |
| API / Service / Repository / Migration 真实数据库集成，`-race` | 142 项顶层测试、158 个通过事件，0 失败、0 跳过。记录 `.toolchain/gemini-integration-26141c4fc1fc/result.json` 与同目录 `tests.jsonl`。 |
| 真实数据库备份恢复与 HTTP 验收 | 31 个通过事件，包含同一刷新令牌并发请求恰有一个成功、复用后新令牌也被吊销的断言。记录 `.toolchain/backup-restore-db-7429340db7c0/result.json`。该轮测试二进制早于最后 UUID/填充/API 覆盖范围小修；不将它计作这些后续小修的回归证据。 |
| Linux 集成代码静态检查 | `GOOS=linux CGO_ENABLED=0 go vet -mod=readonly -tags=integration ./...` 通过。 |
| 前端类型、单测、生产构建 | 全部通过；15 个测试文件、74 项单测通过。日志 `.toolchain/gemini-frontend-unit.log` 与 `.toolchain/gemini-frontend-build.log`。 |
| 浏览器回归 | 生产构建预览、模拟 API：modern-web、reader-loading、gemini-regression 三组共 19 项通过，0 跳过、0 不稳定。记录 `.toolchain/gemini-playwright.json`。包括草稿放弃、迟到进度确认、网关缓存回退与 403 禁止回退、失败章节不污染旧进度。 |
| 备份冻结门禁 | `scripts/verify-backup-contract.py` 的 65 项检查、16 份冻结文件摘要通过。 |

集成使用自建、无外部端口的临时 PostgreSQL 16.15 / Redis 容器及合成数据，两个结果文件分别确认 3 个和 4 个临时容器全部清理。普通后端测试的“需要数据库而跳过”情况与单独真实数据库集成验证分开报告，不合并成全仓零跳过。

## 回归中发现并纠正的问题

直接改用 `NewDocumentFromNode` 的优化会改变根节点自身选择语义，首次黄金样本下降至 88.6%；已撤销该优化，最终恢复 90.9%。首次失败日志保留为 `.toolchain/gemini-backend-race-first.jsonl`，未降低门槛。

浏览器新增错误页场景暴露清理步骤的“返回书架”选择器歧义；修正为阅读页头链接后，最终 19 项全部通过。没有据此修改业务断言。

## 未覆盖及后续工作

后续进展：统一 hostname 与书源准入已由独立模块继续完成，见 [组合准入验证](source-admission-verification-2026-11-14.md)。下述事项反映本轮最初整改交付时的边界，测试数字仍仅属于当轮实现。

完整 AES/DES 互操作、跨请求的源/域名统一限流、覆盖规则试抓事务、沙箱进程/内存隔离、跨页会话策略、大文件性能、真实书源/移动设备验收，以及正式备份恢复 CLI、交接和 S3 仍是独立待办。现有测试不证明慢 NAS 可立即取消、沙箱不会 OOM，或所有真实来源兼容。

资料包由 `python scripts/package-gemini-audit.py` 从当前工作树生成，目录为 `.toolchain/gemini-audit-时间戳-随机串`；先读其中 `START_HERE.md`。ZIP 与全文/分段是替代输入，不应同一批重复上传。生成包是本地复审材料，未上传 Gemini。打包排除依赖、构建产物、参考仓库、私有环境文件和真实书样；有限密钥特征扫描不能代替人工敏感信息检查。
