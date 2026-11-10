package parser

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// buildBenchHTML 构造 n 个书节点的 HTML（纯计算基准，无 DB / 网络）。
func buildBenchHTML(n int) string {
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b,
			`<div class="book"><a class="title" href="/book/%d">书名%d</a>`+
				`<span class="author">作者%d</span><span class="intro">简介%d</span>`+
				`<span class="kind">玄幻</span><span class="lastChapter">第%d章</span></div>`,
			i, i, i, i, i)
	}
	b.WriteString("</body></html>")
	return b.String()
}

// BenchmarkEvalListAndFields 对 20 节点 Mock 结构完整执行列表切分与 7 个字段抽取。
func BenchmarkEvalListAndFields(b *testing.B) {
	e := newTestEngine()
	content := buildBenchHTML(20)
	ctx := context.Background()

	fields := []string{
		".title@text",
		"a@href",
		".author@text",
		".intro@text",
		".kind@text",
		".lastChapter@text",
		`.title@text##\s+##`,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nodes, err := e.EvalListNodes(ctx, ".book", content)
		if err != nil {
			b.Fatalf("EvalListNodes: %v", err)
		}
		if len(nodes) != 20 {
			b.Fatalf("want 20 nodes, got %d", len(nodes))
		}
		for _, node := range nodes {
			e.ResetVars()
			for _, f := range fields {
				if _, err := e.EvalField(ctx, node, f); err != nil {
					b.Fatalf("EvalField(%q): %v", f, err)
				}
			}
		}
	}
}
