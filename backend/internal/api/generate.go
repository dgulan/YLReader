// Package api 承放由 docs/openapi.yaml 经固定版本 oapi-codegen 生成的
// DTO、ServerInterface 与 chi 路由绑定，见 generated.gen.go。
//
// 生成的代码必须提交进 Git（Docker 构建不依赖外部 codegen 工具链）。
// OpenAPI 契约变更后，手动执行下面的生成指令重建（不参与 go build / 自动构建）：
package api

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 -generate types,chi-server -package api -o generated.gen.go ../../../docs/openapi.yaml
