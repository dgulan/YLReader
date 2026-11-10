//go:build integration

package api_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain 以 goleak 校验 api 包集成测试无协程泄漏。
// 白名单（纯 Go 字符串，无 Markdown）：pgxpool 后台健康检查协程随连接池常驻，
// 属测试基础设施而非业务泄漏，理由登记于 docs/tech-debt.md DEF-CODE-08 白名单段。
//
// httptest 的 http.Client 连接：newTestServer 的 t.Cleanup(ts.Close) 关闭
// Server 后，测试中使用的 http.Client（含 DefaultClient 与 doJSON 内部请求）
// 的 persistConn 读到 EOF 自动退出，不残留读写协程；因此白名单只覆盖
// pgxpool 后台协程，无需 net/http 条目。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).triggerHealthCheck"),
	)
}
