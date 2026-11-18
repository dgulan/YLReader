# Gemini 审计修复验收（2026-11-17）

逐项分类及保留待办见 [复核记录](gemini-audit-recheck-2026-11-17.md)，先行设计见 gemini-audit-remediation-design-2026-11-17.md。本轮修复业务分页、输入边界、前端异步状态、存储原语及恢复引用验证，不代表完成全部审计建议或达到发布条件。

## 后端

- 允许本机合成回环套接字后，go test -json -count=1 ./... 返回0：297个顶层通过，0失败。7个具名测试/子测试因专用Redis、数据库或XPath辅助进程条件跳过；另3个包无测试。日志 .toolchain/gemini-audit-20261117-backend.jsonl。没有把这些跳过项当作本轮已验。
- 该全量后又加入服务端缓存策略修订及回归测试，单独重跑 service 最终26个顶层通过，0失败，日志 .toolchain/gemini-audit-20261117-service-final.jsonl；导入非法非数字端口的最终调整后 repository 6个顶层回归与包级vet再次通过。完整go vet -tags=integration ./...通过。未跑race或全业务数据库集成套件。
- 新回归验证：后续页断连/超时/503/空页/循环/页数耗尽均无部分正文，正常两页拼接；缓存摘要不再匹配旧分页策略；API/导入URL拒绝凭据、私网字面量及非法端口（保留Legado特殊标识）；Header非Object不能写入/导入；身份文件词法等价；代理在已核验同次DNS集合内故障切换。
- 首次受限环境全量尝试出现 connectex 本机套接字禁止，相关网络测试失败、其他需要本机连接的测试等待；已终止该次未完成进程，使用授权的本机测试权限完成上述回归。没有改产品逻辑掩盖环境问题。

## 前端

npm run type-check、npm test、npm run build 最终全部通过：19个测试文件、95项测试。新增覆盖缺会话挂载不发请求、Pinia销毁卸载监听、切书取消及晚到结果不污染、进度查询晚到不向新书提交、失去账号不重用弹窗、巨型文件不进textarea、读取错误/旧文件晚到/旧预检失效。

早期新增测试曾有ImportResult夹具缺字段及jsdom未提供dialog方法的问题，已修正测试夹具并在最终完整回归通过；没有用jsdom冒充真实Safari。未重跑浏览器端到端、真机或Nginx镜像验收；Nginx本轮只加维护注释。

## Linux 安全存储

.toolchain/gemini-audit-storage-linux.test 在postgres:16基础镜像、自有非root1000:1000、无网络、只读根、只读二进制挂载和私有tmpfs下运行全包：22个顶层、含子项24个通过事件，0失败/跳过。新增前置目录权限失败两次仍返回ErrUnsafeStorage、修正后可提交的测试；既有开始提交后冲突/同步未知仍不可重放且保留数据的回归保持通过。

日志 .toolchain/gemini-audit-storage-linux.log，二进制 SHA-256 b21140d938b1388c9cb5cc5a9b12599a61e4c66eb0de5cdde687e7851e8c2cc8。容器自动删除。前两次测试容器调用分别因只读根拒绝docker cp、PowerShell未引用-test.v参数而没有运行测试，均已清理；最终改只读挂载与正确引用参数。

## 真实归档/数据库联合链

```text
python scripts/verify-backup-restore-db.py --binary .toolchain/gemini-audit-backup-linux.test --handover-data --handover-behavior --handover-register --behavior-accounts --behavior-dispatch --behavior-ledger --behavior-http --behavior-review
```

最终 .toolchain/backup-restore-db-06a02914866a/result.json：passed=true，127个通过事件，无失败/跳过，四个自有临时容器cleanup均true。新增orphan目标库含两本书，却有第三份无数据库引用的本地正文对象，确认ErrObjectReferences、数据库回滚至pristine及已确认失败的暂存清理；正常正文恢复、Header认证、对象发布、故障保留与真实TLS行为仍通过。通用部分报告固定10 pass/2 unavailable，未推进切换或生产执行权限。

| 证据 | SHA-256 |
|---|---|
| Linux备份测试二进制 | 04c04bcb1cfb41f410d2a8de445b07fd5885646552b903e8192f1275270b1016 |
| 合成数据库归档 | 3f40e6e0fe456c7636e31d3d3bb0588f87ba27e327e9d92bd5302266724f6303 |
| PostgreSQL16.15镜像 | f9a7b9ebf6f56201f949db745e47bc423c0f0f8294bafe991cc10bf74b029657 |
| Redis镜像 | c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b |

该备份二进制包含本轮恢复/引用/存储改动；后续只增加服务缓存摘要及导入非法端口修订，分别经上述本机包回归，不据此声称联合链覆盖其数据库业务路径。

失败记录保留：

- .toolchain/backup-restore-db-f8fcdbcbcd45/：新增orphan目标最初未加入编排建库清单，导致用例收到通用数据库错误；补齐脚本建库及通过事件计数。
- .toolchain/backup-restore-db-fb8da7261774/：orphan真实回滚已通过，但既有headeraad在StageRecorded收到ErrPlan，dispatch_intent_ack的意向/账本诊断不满足预期。未证明根因；不将其直接归因于并发构建或负载，也未放宽门禁、超时、重试或筛掉用例。其他任务结束后原样完整复跑获得最终127通过。

两次失败运行四个容器均清理成功。最终通过不能抹除第二次间歇失败：**验收稳定性仍待定位，尤其是计划复核/意向写前核验的可观测性及受限时限；正式发布前需要解释或复现实证收敛。** 不把未知意向自动升级成成功，不对未知真实操作重放；本处重跑只在全新独立合成目标进行。

## 契约与边界

65项契约检查、16份冻结摘要一致，Python编译、gofmt及git diff --check通过。未访问references或业务库，未用真实书源/用户执行认证探测。未部署、提交、推送、合并、生成YLReader-0或更新Gemini上传包。AES桥接、worker探针、静态Header出网、大包容量、持锁I/O及真机/ARM/NAS等继续按复核记录单列，未承诺已解决。
