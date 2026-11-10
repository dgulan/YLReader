// Package api 承放由 docs/openapi.yaml（Frozen v1.1）经 oapi-codegen 生成的
// DTO、ServerInterface 与 chi 路由绑定，见 generated.gen.go。
//
// 生成的代码必须提交进 Git（Docker 构建不依赖外部 codegen 工具链）。
// OpenAPI 契约变更后，手动执行下面的生成指令重建（不参与 go build / 自动构建）：
package api

//go:generate cd /d ..\..\.. && oapi-codegen -config docs/oapi-codegen.yaml docs/openapi.yaml
