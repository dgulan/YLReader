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
| docs/architecture.md | b58c64696ef5310863244b64fd0c6a62b876d7098b1f07c1b2451853ccccaaa9 |
| docs/schema.sql | fa26a8e596a4f2c89bd2ef9713ff1028aeb42925377c25ae26f957a165ba07b0 |
| docs/openapi.yaml | 98d9425aa38e5b239b48e19dd81572c469380e96b2b4fd45ed1377982b9caddc |
| docs/research/1432433gg_reader-next.md | f47d1e87516b6980d86ed7a4b84f3db526770f422c4786b7aa1df5a2c65cb678 |
| docs/research/changshengyu_openreader.md | 909606b90fe636aeac64ac0cd2e745d960915bba534a5bcae0c2e248778bcde8 |
| docs/research/changshengyu_reader-dev.md | eaf8a857590c548a3a835c45309f5615f0c6d711afc8bb1649ef8caa8256d9fa |
| docs/research/givenge_reader-rust.md | 28329b84333a1788eeaaea9418630ea6705c016d9a3d569f6973dfefe658ceb7 |
| docs/research/kukume_legado-web.md | beb9abf848cce686a0a610dd618501acfd165977fcd287e231eb1b1ddfd9013b |
| docs/research/summary.md | 891befc65a48d99cff966ee176353ee8b3b1ad12c5934bd0df936b311ddf09aa |
| docs/research/warpdotsys_reader-dev.md | 1e023d2ec33a6d00d8073ba633d2c639db7d55eaffbf4d91598c20af586fe6fe |


## ADR-017 修订 1（2026-11-13）

版本化迁移设计先冻结后实现；新增账本及基线登记，readyz 增加版本/校验和要求，API 43 个操作保持不变。以上摘要中三份核心契约更新为本修订；研究未改，发布仍未封板。依据 migration-design.md，验证见 migration-verification.md。


## ADR-018 修订 2（2026-11-13）

同步导入预算设计先冻结后实现；新增 import_timeout/504 与预算配置，默认90s保持不变。DDL、数据库版本1及已发布迁移基线校验和不变；schema.sql 仅登记无DDL变化注释。API仍43个操作，见 import-budget-design.md 和 import-budget-verification.md。

## 跨平台换行补记（2026-11-13）

发布前验证发现Windows工作树中的冻结文档含CRLF，而Git自动转换会令Linux检出摘要不同。仅将冻结研究/架构/OpenAPI文档统一为LF，并更新上表摘要；.gitattributes约束文档换行。内容、Frozen v1.5修订2语义、API 1.0.0、数据库版本1及已发布SQL迁移基线均不变。

## 项目更名补记（2026-11-13）

按用户指定，将项目展示名、文档和路径改为YLReader（D:\YLReader），远端为https://github.com/dgulan/YLReader。仅展示名称和文档注释变化，更新本记录摘要；API操作、DDL、数据库版本不变。已发布001_baseline.sql字节与校验和保持不可变，其历史产品标签保留；基线对照测试只归一化这一处标签注释。Go模块、IndexedDB、环境变量、数据库/存储标识保留兼容名；Compose显式name: yreader，避免目录改名误建空卷。
