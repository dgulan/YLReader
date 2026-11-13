# v1.5 设计冻结记录

日期：2026-11-12。状态：Frozen v1.5；OpenAPI info.version 保持 1.0.0。本次为现有设计基线冻结，不是发布批准。适用的研发约定由 ADR-016 记录，后续开发不改变冻结端点和数据语义。

## 范围和证据

研究六仓行为及汇总、architecture、schema 和 43 个 API 操作。前端按现有契约实现；BookCreate 的 name 与实际校验统一为必填，客户端必须传入名称，readyz 说明改为实际 PG/可选 Redis 探针。

既有隔离数据库及 Linux race：171 个顶层测试全部通过，0 失败/跳过；生产容器与在途请求排空、文件卷持久化、旧视图升级通过，详见 verification-2026-11-12.md。补齐 40 处接口说明后 Spectral 为 0 error/0 warning，生成类型同步；这一轮名称字段类型调整另经后端编译与回归验证。此前完整 race 是说明/必填字段同步前的证据，不声称重新执行。

## 执行约定

前端使用 strict TypeScript、生成类型、Vitest 和 Playwright；核心阅读器不引入 Element Plus 布局/弹窗。按垂直流程交付并运行对应测试；API/实现/验证分组组织变更，不自动提交或发布。

## 独立延期

- A2 删除历史 chapter_cache 表：未授权，继续不执行，不列为已闭环。
- S3 驱动、旧版数据迁移 CLI、完整前端与全产品性能/兼容性验收：开发及发布待办。
- GitHub Actions 首次日志：已有本机 Linux race 证据，不冒充 CI 已执行。
- 依赖审计：历史三项 moderate 已随前端工具链修订消除，当前 audit 为 0 漏洞；未执行强制依赖修复。

## 冻结文件摘要

摘要不包含本记录；后续更改冻结文件须记录原因及验证。

| 文件 | SHA-256 |
|---|---|
| docs/architecture.md | cc07a5021fdf992723dffb721d07fda9ed1ee63a57f814c00fda922be77acf3a |
| docs/schema.sql | 51dce055e7f7363f88433c067484ab75334c298f1078991400486c30d0942c3b |
| docs/openapi.yaml | dbf2a02adf2e1d5497d2ed46aba25b68c9193bf454975407b207f8cdececb503 |
| docs/research/1432433gg_reader-next.md | f9a2d19e736f3df8a130dba13f31fa2d73c2589eeef23308970c1b7ad3a82568 |
| docs/research/changshengyu_openreader.md | 56e25f4cce3ee4ea2e696fdb9d496fecb438df143971a1e29c2b7a7854038f52 |
| docs/research/changshengyu_reader-dev.md | 68bf7b1f9bcdca41aa73c416dbb41a98db4b6a7a29c7243b1ffb5c10d74e16d0 |
| docs/research/givenge_reader-rust.md | b0760b5923f1313b90ccfa10147eb72026df1bb0984adcf47371ee77270782be |
| docs/research/kukume_legado-web.md | cfe8ee5c93fd8aa3f2a017c05770c98903d0c1f485e372c5e61d50388148bcac |
| docs/research/summary.md | 9ab80243ca20f70dfade3a75f06406110c3b8fc5f9b4e9eb482a48d60b3dd927 |
| docs/research/warpdotsys_reader-dev.md | b0ca90c399a3edb9ba71836b24f2b5a682a21e0882aaab3bda0fc08fb55167d4 |


## ADR-017 修订 1（2026-11-13）

版本化迁移设计先冻结后实现；新增账本及基线登记，readyz 增加版本/校验和要求，API 43 个操作保持不变。以上摘要中三份核心契约更新为本修订；研究未改，发布仍未封板。依据 migration-design.md，验证见 migration-verification.md。


## ADR-018 修订 2（2026-11-13）

同步导入预算设计先冻结后实现；新增 import_timeout/504 与预算配置，默认90s保持不变。DDL、数据库版本1及已发布迁移基线校验和不变；schema.sql 仅登记无DDL变化注释。API仍43个操作，见 import-budget-design.md 和 import-budget-verification.md。

## 跨平台换行补记（2026-11-13）

发布前验证发现Windows工作树中的冻结文档含CRLF，而Git自动转换会令Linux检出摘要不同。仅将冻结研究/架构/OpenAPI文档统一为LF，并更新上表摘要；.gitattributes约束文档换行。内容、Frozen v1.5修订2语义、API 1.0.0、数据库版本1及已发布SQL迁移基线均不变。
