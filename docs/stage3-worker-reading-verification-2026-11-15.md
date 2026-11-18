# 第3阶段：受限worker真实书源复核

本轮不部署、不提交、不推送，不访问生产数据。使用用户提供合集的猫眼看书，关键字为公版《三国演义》，未登录上游、未调用浏览器过盾、未批量下载、未更改规则以制造成功。

## 隔离与证据边界

新增 `scripts/verify-stage3-worker-reading.py`。临时API/PG/Redis使用独占Docker网络，API仅发布回环随机端口；worker无网络、128MiB/1CPU/64进程、非root、只读根文件系统、cap-drop ALL及no-new-privileges。IPC为本轮0700卷，API只读挂载；规则HTTP仍由父进程现有Fetcher进行SSRF过滤。当前源码Linux静态API/worker只读挂载到已有镜像，报告记录镜像ID和二进制SHA；不是最终镜像重新构建或ARM真机验收。

所有轮次确认远程worker启用及readyz成功，运行后API可用、worker running=true/OOM=false/restarts=0，7项专属容器/卷/网络清理全部true。日志不保存认证头、口令或正文。结构探测通过同一个Fetcher和远程worker执行自有JS，仅返回JSON类型和字段名，不能代替原书源规则验收。

## 发现与修复

| 轮次（位于.toolchain） | 真实结果 |
|---|---|
| stage3-worker-reading-eea75733852f | 导入200，搜索total=1，但没有精确匹配公版书，未进入加书/目录/正文 |
| stage3-worker-reading-8754f953acef | 确认唯一候选name为空且没有book_url；独立结构探测200，root含request_id/msg/code/data，data非数组，没有书籍字段。两次HTTP抓取各自独立，不能当作同一响应快照 |
| stage3-worker-reading-2266682f0ae8 | 修复后搜索200、total=0、items=[]；后续结构探测504/source_timeout，不能记为探测成功 |

**纠正历史结论**：前次“猫眼搜索1个结果、0失败码”只检查total，没有检查必填字段，是验收误判；不能算作有效书籍搜索成功。目前没有猫眼完整阅读通过证据，更没有150条书源运行兼容证据。合集导入预览成功这一事实保持成立。

修复1：搜索候选需非空书名和HTTP(S)绝对URL，否则不进入items/total；与现有加书校验一致，保留相对URL解析、可选字段空值策略及字段变量求值顺序。不猜测各站点JSON业务错误码含义。

修复2：字段求值预检复用标准管线的模板分类，使JSON节点上的 `/novel/{{$.id}}` 得到实际地址，而非提前被视为XPath拒绝。真正XPath在JSON节点上仍为unsupported_rule；没有放宽JS沙箱或SSRF。

新增原创JSON单测先失败后通过，覆盖错误对象/缺失字段/危险协议、混合合法记录/模板，以及真正XPath仍被拒绝。Windows解析/服务/API包普通测试通过；Linux race集成结果另行记录。公开API和DDL未变，65项契约检查、16个冻结哈希匹配；Python编译、diff检查通过。旧Windows验收脚本也改为核对有效结果数，不再仅凭total认定成功。

最终Linux race集成证据 `.toolchain/gemini-integration-6c9c0c17cd14/`：API、service、repository、migration四包通过，153项顶层测试pass，0 fail、0 skip；API包281.552秒、service 8.853秒、repository 40.996秒、migration 2.494秒（不含编译）。PG/Redis和测试进程共享本轮无外网网络命名空间，使用当前schema的独占yreader_test，3项清理均true。该回归不包含浏览器或真实书源成功声明。

## 未完成项

本轮 stopped_at=search_no_exact_public_domain_match、full_reading_passed=false，命令返回非零；基础设施通过不等于真实阅读通过。未执行真实目录JS/AES和正文，不宣称AES桥接兼容；已有实现中未找到该书源使用的aesBase64DecodeToString方法，后续须单独冻结支持范围并用原创向量验证，不能为了当前站点盲目增加桥接。

第3阶段仍待可正常返回书籍的合法书源、完整目录/正文、多端阅读、真实iPhone15Pro及较长阅读；生产CSP和原生ARM验收继续待办。

复现：先以当前源码构建linux/amd64、CGO_ENABLED=0的server和sandboxworker，再执行：

```text
python scripts/verify-stage3-worker-reading.py --server .toolchain/stage3-server-linux --worker .toolchain/stage3-worker-linux --sources-dir .toolchain/stage3-user-sources
```

只用本地已有镜像，不拉取；输入合集留在忽略目录，不加入审计源码包。报告明确区分infrastructure_passed、full_reading_passed与cleanup_passed。

后续补充了GET/UTF-8表单POST请求选项解释，及另外三个候选的低频复核；结果和拒绝边界见 [请求兼容验证](search-request-compatibility-verification-2026-11-15.md)。额外结构探测现需显式 `--probe-json-shape`，仅对普通JSON GET有效；上述历史证据不变，不能将有请求选项的URL当作诊断GET重放。真实目录/正文门禁仍未通过。

用户随后指定测试“鬼吹灯”，已复核原源及另一个公开合集的4个新候选，均未返回有效书籍；固定公网HTTPS对照成功。新脚本支持显式关键词/精确书名和仅搜索模式，完整证据见 [鬼吹灯搜索复核](stage3-worker-ghost-search-verification-2026-11-15.md)，不把搜索模式的passed当作完整阅读通过。
