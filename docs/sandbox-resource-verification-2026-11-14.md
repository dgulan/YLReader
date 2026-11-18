# 沙箱资源边界开发与验证（2026-11-14）

后续独立 worker 接入与真实 IPC/OOM 重启证据见 [worker 验证记录](sandbox-worker-verification-2026-11-14.md)。本文保留该阶段的历史结论。

对应 [设计契约](sandbox-resource-design-2026-11-14.md) 与 architecture ADR-020。本轮交付进程内执行约束、两处真实边界修复和独立隔离实验工具；不是生产 worker 全部完成。保留此前未提交工作，未部署、提交、推送、合并；未读取 references，未触碰持久业务数据库。

## 修复与行为

每次新建 Runtime，仅缓存 Program，防止脚本新增全局、词法变量、原型和桥接修改残留到下一次执行。超时监视延伸到 result getter、toString 和结构化导出；监视结束后才释放执行名额。默认四个并行执行、250ms 排队、3 秒脚本预算，加入源码与输入数据预算、JSON 导出节点/深度/编码预算，并限制为 JSON 数据。

原生工作不响应取消时仍占用名额，不启动遗留执行 goroutine；这避免逻辑上“已释放”而实际仍在消耗 CPU。Go panic 返回固定错误，不回显 panic 内容。Program 编译移出缓存锁，缓存命中不被正在编译的其他规则阻塞。基准代码、回归测试、cmd/sandboxprobe 和可复现的 scripts/verify-sandbox-isolation.py 均纳入仓库。

## 验证

| 范围 | 结果与证据 |
|---|---|
| Windows 定向资源/探针测试 | 通过。覆盖全局及原型隔离、输入桥接保护、转换/兜底 getter 超时、循环/巨数组/过深导出、Go callback 输入拒绝、原生工作名额持有、缓存锁边界、单请求探针协议。 |
| 首轮完整 Linux 后端 race | 186 项顶层通过，含子测试 437 个通过事件，0 失败；4 个实际条件跳过（XPath 子进程 helper 和三个需要 TEST_DATABASE_URL 的测试），无测试包 skip 另算。记录 `.toolchain/sandbox-resource-bb51b391ddd0/tests.jsonl`、`result.json`。早于最后队列截止检查及 panic/取消测试补强，不冒充最终代码全部重跑。 |
| 最终 sandbox/parser/server/probe race | 48 项顶层通过，含子测试 121 个通过事件，0 失败；一个 XPath 子进程 helper 跳过。记录 `.toolchain/sandbox-resource-final-2f580745d2d2/tests.jsonl`、`result.json`。包含最后生产代码、探针协议和新增测试，两个临时容器均清理。 |
| 黄金解析样本 | 最终保持 40/44（90.9%），6 个显式不支持；未降低 90% 门槛。 |
| 真实数据库 integration race | API / Service / Repository / Migration 最终 147 项顶层、168 个通过事件，0 失败、0 跳过。记录 `.toolchain/sandbox-resource-db-acfa255bb888/tests.jsonl`、`result.json`，三个自有临时容器全部清理。 |
| 静态检查 | Linux 全包 integration 标签 go vet 通过，日志 `.toolchain/sandbox-resource-vet.log`。 |
| 冻结/格式 | 主冻结表 10 份文件摘要匹配；architecture 按 ADR-020 登记新摘要。备份 65 项检查及 16 份独立冻结摘要通过；git diff --check 通过。公开 API、DDL、研究及数据库版本未改，不需要业务迁移。 |

各轮范围有重叠，不相加作为独立测试总数。前端未改，本轮未重复浏览器验收。此前各轮备份验收不被这里的普通 Go 测试替代。

## 独立进程实验

最终执行的是仓库内 `python scripts/verify-sandbox-isolation.py --image yreader-pre-release:race`，固定使用本地解析出的 image ID。Linux CGO-disabled probe 二进制 SHA-256 为 `7fe9db1e128eb4992ceceebbc94eaf29ed6b37120789c88293aed552a780087b`，未注入 HTTPClient 或服务凭据。

六个案例：正常返回、结构化返回、过长源码拒绝、CPU 死循环超时、大分配 OOM、新进程恢复。全部在无网络、只读、非 root、cap-drop ALL、no-new-privileges、pids=64、128MiB 内存且 memory-swap 与 memory 相同的临时容器执行，并检查实际配置。大分配只在独立受限进程内执行，没有在 API 或宿主测试进程中运行。

大分配为 256MiB 原生字符串请求，实测 OOMKilled=true、退出码 137；随后独立新进程正常返回 recovered。六项通过，六个容器全部清理，记录 `.toolchain/sandbox-isolation-1e5b58f84c2c/result.json` 与同目录 stdout/stderr。早期同类实验记录保留，但不累计为额外覆盖率。

此证据仅证明选定容器配置下的故障隔离和新进程恢复，不证明正式 API 已具有逐次执行内存硬上限、自动 worker 重建或完整 IPC。

## 当前成本与剩余门禁

在当前 Linux/Go 1.26.11、i5-13500 环境的 200ms 串行微基准：FreshEval 477801 ns/op、17499 B/op、195 allocs/op；StructuredExport 153384 ns/op、19518 B/op、222 allocs/op。记录 `.toolchain/sandbox-benchmark-da1f4d2ba1/benchmark.txt`、`result.json`，临时容器清理。运行时与其他验证有负载重叠；不是隔离性能对比、生产 p95 或内存峰值承诺，没有旧版同条件基线，不能宣称性能提升。

下一步是正式 worker 的有界双向 IPC 与父进程出网代理，确保可信源身份、SSRF、双层准入和取消机制继续成立，再做崩溃/强杀/重建、输入所有权及 p95/吞吐验收。当前单进程单次 native 分配仍可能拖垮 API；预算、GOMEMLIMIT 和本次独立实验都不能把该剩余风险标成已闭环。
