# YLReader 项目更名（2026-11-13）

用户明确指定远端为https://github.com/dgulan/YLReader，并要求本地目录与文档同步改名。

- 完整项目、Git历史、参考目录与忽略的本地工具/缓存均已迁到D:\YLReader。旧D:\YReader仅剩当前会话占用的空目录；没有遗漏源码或数据，也没有把参考代码引入业务目录。当前Codex对话的目录绑定不能通过managed-worktree工具修改，因为这是普通工作区；后续请从新目录打开项目。
- 文档、AGENTS路径、API展示标题、页面标题和导航展示名改为YLReader；重新登记冻结文档摘要。43个操作、API 1.0.0、数据库版本1和字段语义保持。
- 已发布001_baseline.sql不可变，其历史标签注释和SHA256继续保留。对照测试仅归一化权威文档中的这一处产品标签注释，仍严格比较其他字节、登记哈希与DDL。
- Go模块yreader/backend、环境变量YREADER_HEADER_*、数据库与表、存储路径、sessionStorage/IndexedDB等持久标识保留兼容名称，避免现有用户退出或丢失离线记录。
- Compose显式name: yreader，避免根目录改名使原命名卷被误认为不存在。不执行生产compose up或迁移。
- 新仓库实际为公开；仅提交已检查的源码/文档，.env、凭据、工具缓存及references继续忽略，不上传。
