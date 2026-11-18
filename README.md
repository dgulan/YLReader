# YLReader

独立设计的自托管阅读器，兼容部分 Legado 书源规则。Go/PostgreSQL/Redis 后端，Vue 3 阅读界面。当前处于开发验收阶段，尚未正式发布。

- [架构与设计](docs/architecture.md)
- [接口契约](docs/openapi.yaml)
- [开发部署说明](docs/deployment.md)
- [最新发布就绪审计](docs/release-readiness-audit-2026-11-14.md)
- [审计复核与整改](docs/release-readiness-recheck-2026-11-15.md)
- [第3阶段阅读验收](docs/stage3-reading-verification-2026-11-15.md)
- [worker 运维与限制](docs/sandbox-worker-operations-2026-11-14.md)

开发检查：在 backend 运行 `go test ./...`；在 frontend 安装锁定依赖后运行 `npm run type-check`、`npm test` 和 `npm run build`。浏览器测试需要隔离测试后端及测试账户，见 `.github/workflows/frontend.yml`，不能指向已有用户数据。

现有 Compose 为开发配置，不能直接作为公网生产配方。独立 worker 保持显式启用；ARM 原生、真实书源/移动真机、备份恢复生产闭环与生产升级仍有验收门禁。升级前备份并先在隔离环境演练，不对生产库执行当前测试迁移命令。

本仓库尚未选择分发许可证；公开可见不等于已授予开源使用许可。参考仓库与第三方依赖不改变本项目代码的授权状态。
