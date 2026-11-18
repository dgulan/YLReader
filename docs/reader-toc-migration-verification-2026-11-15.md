# 真实目录迁移演练验收

本轮完成最新严格分页语义下的真实末页复验，以及临时账号112→1202目录迁移。未部署、未提交、未推送，未连接用户reader实例或已有YLReader账号，未读取小说正文。设计见[迁移演练设计](reader-toc-migration-design-2026-11-15.md)。

## 真实末页

使用当前源码重新构建Linux server/worker。server SHA256 `cb5d54e8c1c0c13b32627e49b2aed633fcf2a3042c9ee04245c2b1976392284a`；worker SHA256 `ad0c8f8bc651946b9b47ce0c586861463c7caed761aaa5523cf5833e3e8ba556`。

`.toolchain/stage3-worker-reading-3483e4dfe8ca/`：抖音小说显式分页适配副本，搜索精确书名《顶级气运，悄悄修炼千年》/任我笑成功；单独核对第13页，存在1201–1202分页选项，未提取到下一页链接。完整目录HTTP200、7.885秒、1202项且URL唯一。严格循环/空页保护并未阻止正常末页，无需调整先前适配规则。仍沿用10秒总预算、20页上限，未放宽TLS/SSRF或worker资源限制。

适配源SHA256仍为 `32a2598c49396560942931b54ab1b70a751dad079ce2d3dccb2581c5f2cc99a3`；原配置文件保持不变。

## 迁移结果

验收脚本新增显式迁移参数。先导入原始配置，初始目录必须112项；通过公开API创建合成进度和两枚书签，随后导入仅改变显示名称及两项目录规则的适配配置。始终使用同一临时用户、书籍ID及书源ID。没有修改真实用户数据。

| 证据目录（.toolchain内） | 原目录 | 新目录 | 新目录耗时 | 迁移检查 |
|---|---|---|---|---|
| stage3-worker-reading-5857bd82d4c7 | 112 | 1202 | 7.918秒 | 9项检查全true |
| stage3-worker-reading-bec182269824 | 112 | 1202 | 9.501秒 | 增加书籍/活跃源不变检查后10项全true |

最终轮：进度由旧index=0移动至新index=1201，URL仍为同一第1192章；偏移17、比例0.125、客户端时间与测试设备保留，version由2变3。两枚书签分别0→1201、12→0，指向各自原URL；ID、偏移、标题、摘录、备注和创建时间全部保留。book.version增加，同一书籍和活跃源不变。旧If-Match=2再次PUT返回409，随后GET与迁移后进度完全一致。

脚本仅记录URL摘要、index和断言，不保存令牌/密码、请求头、正文或完整章节URL清单。增加4项离线单测：正常迁移通过；错误进度、丢备注、换书签ID、接受旧版本、改变活跃源均不能误通过；失败刷新保留旧状态；超出声明范围的规则变更被拒绝。测试通过，Python编译、65项契约检查/16个冻结哈希和diff检查通过。审计打包规则已包含该测试文件。

三轮专属容器/卷/网络清理均为7项true；worker均运行、无OOM、重启0。新目录耗时接近10秒上限，不能由两次迁移通过推断长时间网络稳定性或容量保证；本轮未增加预算或自动重试。

## 保留的失败门禁

三轮均保留 `directory_integrity_verified=false`、`passed=false`，命令非零退出；迁移两轮的 `migration_passed=true` 不替代完整验收。上游仍有1200个带数字章号的条目、2个非此编号条目，唯一章号1190，1–1192中2个编号未出现且10个编号重复。未删除、补写或按编号重排任何章节，未降低原来的严格编号断言。

本轮只验证临时账号上的目录与引用保持，不能算真实长期阅读、完整正文覆盖、iPhone真机或正式发布门禁通过，也不自动向现有账号推广适配。后续需要核对编号异常及来源兼容范围，并完成实际iPhone阅读验收。

复现最终迁移（严格编号门禁仍会使命令非零退出）：

```text
python scripts/verify-stage3-worker-reading.py --server .toolchain/toc-migration-server-linux --worker .toolchain/toc-migration-worker-linux --sources-dir .toolchain/reader-comparison-0d21c086f2/douyin --source-name 抖音小说 --keyword 顶级气运，悄悄修炼千年 --expected-author 任我笑 --directory-probe --expected-directory-count 1202 --expected-numbered-chapters 1192 --migrate-to-source .toolchain/reader-comparison-0d21c086f2/douyin-pagination-adapted/source.json --migration-expected-old-count 112 --migration-bookmark-index 0 --migration-bookmark-index 12
```
