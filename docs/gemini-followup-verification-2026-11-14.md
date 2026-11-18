# 第二轮审计整改验证

输入与逐条结论见 [16 组复核](gemini-followup-recheck-2026-11-14.md)，范围约定见 [设计](gemini-followup-design-2026-11-14.md)。本轮保留此前未提交工作，不部署、提交、推送或合并，不操作生产数据库，不访问参考仓库。

## 修复与证据范围

生产弱 JWT 校验提前且记录脱敏错误；CSS 支持负向排除与排除后位置选择；替换规则的排序整数不再回绕；manifest 括号错配返回 Schema 错误但保留深度/字节限额；认证免重放按 pathname 判断；导入关系列整数不再回绕，完整 rules 原文和旧生态身份值保留。

Windows 定向后端测试 `TestFollowup` 全部通过，覆盖 server、api、backup、repository、parser 五个包。目标包 Linux integration 标签 `go vet` 通过，记录 `.toolchain/gemini-followup-vet.log`。上述检查不等同于完整后端测试或真实数据库执行。

完整 Linux 后端普通测试 `go test -race ./...`：179 项顶层测试通过，含子测试共 430 个通过事件，0 失败。4 个实际测试跳过：TestXPathTerminationWorker（子进程 helper）及三个需要 TEST_DATABASE_URL 的既有测试；另有无测试包的 skip 事件，不合计为测试跳过。黄金样本 40/44（90.9%），6 个显式不支持，保持 90% 门槛。记录 `.toolchain/gemini-followup-647d2a128bba/tests.jsonl`、`result.json`，Redis 与 Go 临时容器均清理。

首次真实数据库回归中 API / Service / Migration 通过，但 repository 的 TestLegadoSamples 失败：原有前缀筛选认定 2073 条待导入，严格 URL 预检只导出 2053 条。记录 `.toolchain/gemini-followup-db-638ea9a4bbf3/tests.jsonl`、`result.json`，三个容器均清理。已撤回过严 URL 预检、补身份保留单测，不修改 TestLegadoSamples 或其预期数量；此后的定向及数据库回归另列。上面的普通完整 race 数字属于撤回前快照，不能替代最终验证。

撤回后的完整 Linux race 再次通过：179 项顶层测试、含子测试 430 个通过事件，0 失败，仍为前述 4 项条件跳过；包含 TestFollowupImportedIdentityAndRulesPreserved 的实际执行。记录 `.toolchain/gemini-followup-970b87e13b44/tests.jsonl`、`result.json`，两个临时容器全部清理。目标包 integration 标签 go vet 也已在最终代码上重新通过。重复执行不累计为新增测试数。

最终真实 PostgreSQL API / Service / Repository / Migration integration race：147 项顶层测试、含子测试 168 个通过事件，0 失败、0 跳过。TestFollowupReplaceRuleIntegerBoundariesPersist 验证 MinInt32 创建、MaxInt32 更新成功，六次越界创建/更新均返回 400，数据库仍只有原来一行且排序值未变。TestLegadoSamples 两份文件分别保留原断言的 2073 和 2548 条合法样本，导入/导出一致。记录 `.toolchain/gemini-followup-db-7cad27d1cfdd/tests.jsonl`、`result.json`，三个临时容器全部清理。本轮全部使用隔离合成数据库，不连接生产目标；源样本数据仅入临时测试实例。

前端类型检查、生产构建通过；15 个文件、77 项 Vitest 单测通过。日志 `.toolchain/gemini-followup-frontend-tests.log`、`.toolchain/gemini-followup-frontend-build.log`。新增三种带 query/fragment/首尾空白的认证路径用例，确认只有一次请求，不续期或重放。

生产构建预览、模拟 API 的浏览器回归共 19 项通过，0 失败、0 跳过、0 不稳定；记录 `.toolchain/gemini-followup-playwright.json`。采用 tests/preview.config.ts 禁用开发 API 代理，预览仅监听本机，完成后关闭本次启动的进程。它不是与真实后台联通或物理移动设备验收。

`scripts/verify-backup-contract.py` 通过 65 项检查、16 份冻结摘要，`git diff --check` 通过；没有改公开 DDL、API、研究或备份冻结格式。

## 保留的边界

未新建 Cookie 会话模型，未实现 goja 堆硬配额或正式恢复切流/CLI；跨标签页刷新协调、封面类型白名单、真实移动设备与恢复未知提交现场收敛仍是独立待办。不能把静态意见或既有测试当作所有当前代码已被执行验证，更不作“无任何漏洞”的保证。

资料包仍从当前未提交工作树生成，不上传外部服务；复核、设计和本记录均纳入 00-context，便于下一轮按文件摘要和具体行号复审。
