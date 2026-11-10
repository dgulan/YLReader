//go:build integration

package api_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain 以 goleak 校验 api 包集成测试无协程泄漏。
// 白名单（纯 Go 字符串，无 Markdown）：pgxpool 后台健康检查协程随连接池常驻，
// 属测试基础设施而非业务泄漏，理由登记于 docs/tech-debt.md DEF-CODE-08 白名单段。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).triggerHealthCheck"),
	)
}
