# 书源与域名组合准入验证（2026-11-14）

对应 [实现契约](source-admission-design-2026-11-14.md)。分支 `codex/pre-release-20261113`，基线 HEAD `fdee4bd1ffa6f73c6da885f2659d7510ae0c30c4`。此前未提交工作保留；本轮未部署、提交、推送或合并，未读取参考仓库或操作生产数据库。

## 本轮交付

- 删除每次搜索临时创建的 sourceLimiter；从可信数据库 BookSource 绑定 UUID 与策略到 Go context。搜索、目录刷新、正文、Worker 预热、调试及 java HTTP 桥接共用生产 Fetcher。
- 纯数字并发上限、n/ms 令牌桶与域名 500ms 间隔组合准入。Redis Lua 和内存互斥锁均先检查两层，再消耗预算；正常拒绝不占另一层名额。
- 并发名额持有到响应体 EOF、Close、取消或截止；20 秒受限请求预算、30 秒租约、独立 1 秒清理。随机 token 幂等释放，旧 token 不删除替代请求；清理失败依靠租约过期。
- 非空策略严格解析并限制为 64 字节；损坏 Redis 类型、缺失 TTL、非法/非有限窗口状态均拒绝。配置 Redis 时不回退内存。
- 运行中非空策略变化不重置状态；数字状态排空后切换，窗口状态空闲到期后切换。空值显式关闭限制的例外与重新开启需排空的边界均写入契约。

## 验证记录

首轮完整 Linux 后端 race：171 项顶层测试、含子测试 412 个通过事件，0 失败。4 个实际测试跳过：一个 XPath 子进程 helper、三个需要 TEST_DATABASE_URL 的测试；两个无测试包的 skip 不计作测试跳过。黄金样本维持 40/44（90.9%），另有 6 个显式不支持。记录 `.toolchain/source-admission-8285c6f8be0e/tests.jsonl`、`result.json`，两个临时容器均清理。该轮早于最后的长度/损坏浮点防御补强，后续定向回归独立报告。

补强后的定向 Linux race（Fetcher、缓存、服务器）：36 项顶层测试、含子测试 67 个通过事件，0 失败、0 跳过，记录 `.toolchain/source-admission-dae863dd86d4/tests.jsonl`、`result.json`，两个临时容器清理。包含真实 Redis 双层拒绝不消费、32 个独立竞争请求恰好两个获准、窗口补充、过期租约回收、旧 token 所有权、损坏状态拒绝，以及实际 java.ajax 书源身份继承。最终新增的完整重试/重定向身份测试另行补验，不将本轮计作它已执行。

最终定向 race：37 项顶层测试、含子测试 68 个通过事件，0 失败、0 跳过，记录 `.toolchain/source-admission-30633f869d91/tests.jsonl`、`result.json`，两个临时容器均清理。包含完整 503 重试→302 跨域重定向→200 正文读取流程：每次都有同一源身份与恰好一个有效租约，最终没有遗留名额。此轮包含最后所有代码与新增回归测试。

Linux integration 标签的目标包 `go vet` 已通过，日志 `.toolchain/source-admission-vet.log`；先前全后端只编译检查通过，不能当作测试执行。内存行为与 JS 身份另经 Windows 定向测试通过。

真实 PostgreSQL API / Service / Repository / Migration integration race：143 项顶层测试、含子测试 164 个通过事件，0 失败、0 跳过。新增服务入口用例的 search、toc、content、worker、debug 五个子用例全部通过，证明独立构造的服务从数据库读取并传播同一源身份与策略；到准入拒绝即停止，不访问真实书源。记录 `.toolchain/source-admission-db-a40f3e3592e3/tests.jsonl`、`result.json`，三个临时容器均清理。各轮测试范围重叠，不相加作为独立测试总数。

最初 Docker 回归在测试容器启动阶段返回 `unexpected EOF` / exit 125，两个临时容器清理记录在 `.toolchain/source-admission-6e0f45a2f44a/result.json`，不是代码测试失败或通过证据。发现 Docker Desktop 已退出后后台启动现有安装，未终止用户进程、删除既有容器或重置 Docker 数据。

最终 `scripts/verify-backup-contract.py`：65 项检查、16 份冻结摘要通过；`git diff --check` 通过。Gemini 复审包脚本已把本模块契约与验证记录纳入引擎安全批次，仍从当前未提交工作树生成，未上传外部服务。

## 范围限制

使用隔离容器与合成数据，无外部端口、源码只读挂载；内存测试不等于多实例验证，真实 Redis 测试只证明单端点脚本及独立客户端竞争。未演练 Redis 故障转移、暂停进程超过租约或真实远端无视断连；不能保证这些情况下第三方服务器的严格执行并发数。旧版实例混跑和关闭限制期间的在途请求亦不在该保证内。

前端、公开 API、DDL 和备份冻结摘要未改；本轮未重跑浏览器。Cookie 隔离、沙箱内存/进程边界、AES/DES 互操作、正式恢复 CLI/S3 仍是独立事项。
