package parser

import (
	"context"
	"strings"
	"testing"

	"yreader/backend/internal/engine/sandbox"
)

const testHTML = `<html><body>
<div class="content">第一章 风起</div>
<ul><li class="li">第二章 雨落</li><li class="li">第三章 云开</li></ul>
<div class="item">正文<strong>加粗</strong>尾部</div>
<a class="link" href="http://example.com/1">链接一</a>
<a class="link" href="http://example.com/2">链接二</a>
</body></html>`

func newTestEngine() *Engine {
	return New(sandbox.New(sandbox.Config{}, nil))
}

func TestSplitRule(t *testing.T) {
	cases := []struct {
		name    string
		rule    string
		splits  []string
		segs    []string
		op      string
		wantErr bool
	}{
		{"and", "a&&b", []string{"&&", "||"}, []string{"a", "b"}, "&&", false},
		{"or", "a||b", []string{"&&", "||"}, []string{"a", "b"}, "||", false},
		{"interleave", "a%%b", []string{"&&", "||", "%%"}, []string{"a", "b"}, "%%", false},
		{"bracket-jsoup", `div[a="x&&y"]&&span`, []string{"&&", "||"}, []string{`div[a="x&&y"]`, "span"}, "&&", false},
		{"bracket-single", `div[a="x&&y"]`, []string{"&&", "||"}, []string{`div[a="x&&y"]`}, "", false},
		{"jsonpath-bracket", `$.data[a&&b]&&x`, []string{"&&", "||"}, []string{"$.data[a&&b]", "x"}, "&&", false},
		{"unbalanced-paren", "(a&&b", []string{"&&", "||"}, nil, "", true},
		{"unbalanced-bracket", "a[b&&c", []string{"&&", "||"}, nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs, op, err := splitRule(c.rule, c.splits...)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (segs=%v op=%q)", segs, op)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if op != c.op || strings.Join(segs, "\x00") != strings.Join(c.segs, "\x00") {
				t.Fatalf("got segs=%v op=%q, want segs=%v op=%q", segs, op, c.segs, c.op)
			}
		})
	}
}

func TestGoldenRules(t *testing.T) {
	cases := []struct {
		name   string
		rule   string
		source string
		want   string
	}{
		// CSS（class./tag./id./text. 简写 + 索引 + 标准 CSS）
		{"css-class", "class.li@text", testHTML, "第二章 雨落\n第三章 云开"},
		{"css-class-index0", "class.li.0@text", testHTML, "第二章 雨落"},
		{"css-class-index-neg", "class.li.-1@text", testHTML, "第三章 云开"},
		{"css-class-bracket-index", "class.li[1]@text", testHTML, "第三章 云开"},
		{"css-tag-attr", "tag.a@href", testHTML, "http://example.com/1\nhttp://example.com/2"},
		{"css-tag-index", "tag.a.0@href", testHTML, "http://example.com/1"},
		{"css-class-content", "class.content@text", testHTML, "第一章 风起"},
		{"css-std-dot", ".content@text", testHTML, "第一章 风起"},
		{"css-std-tag", "div.content@text", testHTML, "第一章 风起"},
		{"css-prefix", "@css:class.li@text", testHTML, "第二章 雨落\n第三章 云开"},
		{"css-double-at", "@@class.content@text", testHTML, "第一章 风起"},
		{"css-attr-selector", `tag.a[href="http://example.com/1"]@text`, testHTML, "链接一"},
		{"css-chained", "class.item@strong@text", testHTML, "加粗"},
		{"css-html", "class.item@html", testHTML, `<div class="item">正文<strong>加粗</strong>尾部</div>`},
		{"css-owntext", "class.item@ownText", testHTML, "正文尾部"},
		{"css-text", "class.item@text", testHTML, "正文加粗尾部"},
		{"css-textnodes", "class.item@textNodes", testHTML, "正文\n尾部"},
		{"css-attr-getter", "class.content@class", testHTML, "content"},

		// XPath
		{"xpath-text", "//li/text()", testHTML, "第二章 雨落\n第三章 云开"},
		{"xpath-attr", "//a/@href", testHTML, "http://example.com/1\nhttp://example.com/2"},
		{"xpath-prefix", `@XPath://div[@class="content"]/text()`, testHTML, "第一章 风起"},
		{"xpath-slash", "//div[@class='content']", testHTML, "第一章 风起"},
		{"xpath-or", "//strong/text()||//div[@class='content']/text()", testHTML, "加粗"},

		// JSON
		{"json-name", "$.data.name", `{"data":{"name":"斗破苍穹"}}`, "斗破苍穹"},
		{"json-list", "$.list", `{"list":["第一章","第二章","第三章"]}`, "第一章\n第二章\n第三章"},
		{"json-prefix", "@Json:$.data.name", `{"data":{"name":"斗破苍穹"}}`, "斗破苍穹"},
		{"json-bracket-index", "$.data[0].name", `{"data":[{"name":"a"}]}`, "a"},
		{"json-list-index", "$.list[1]", `{"list":["a","b","c"]}`, "b"},

		// Regex
		{"regex-group", `:(\d+)`, "第120章 风起", "120"},
		{"regex-full", `:第(\d+)章`, "第120章 风起", "120"},

		// ## 替换
		{"replace", "class.content@text##风起##雨落", testHTML, "第一章 雨落"},

		// 组合（|| / && / %%）
		{"or", "class.nonexistent@text||class.content@text", testHTML, "第一章 风起"},
		{"and", "class.li@text&&tag.a@href", testHTML, "第二章 雨落\n第三章 云开\nhttp://example.com/1\nhttp://example.com/2"},
		{"interleave", "class.li@text%%tag.a@href", testHTML, "第二章 雨落\nhttp://example.com/1\n第三章 云开\nhttp://example.com/2"},

		// $n 字面量回填
		{"dollar0", "$0", "hello", "hello"},
		{"dollar-prefix", "前缀$0", "hello", "前缀hello"},

		// @put / @get 变量
		{"putget", "@put:{name:class.content@text}@get:{name}", testHTML, "第一章 风起"},

		// {{ }} Fast-Path
		{"template-const", "{{1+2}}", "", "3"},

		// JS 块（<js> 切分）
		{"js-block", `class.content@text<js>"["+result+"]"</js>`, testHTML, "[第一章 风起]"},
	}

	e := newTestEngine()
	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := e.Eval(ctx, c.rule, c.source)
			if err != nil {
				t.Fatalf("Eval(%q) error: %v", c.rule, err)
			}
			if got != c.want {
				t.Fatalf("Eval(%q)\n  got  %q\n  want %q", c.rule, got, c.want)
			}
		})
	}
}

func TestEngineVariables(t *testing.T) {
	e := newTestEngine()
	ctx := context.Background()
	e.SetVariable("x", "value")
	got, err := e.Eval(ctx, "@get:{x}", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != "value" {
		t.Fatalf("got %q want %q", got, "value")
	}

	e.SetVariable("a", "3")
	e.SetVariable("b", "4")
	got, err = e.Eval(ctx, "{{a+b}}", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != "7" {
		t.Fatalf("got %q want %q", got, "7")
	}
}

// TestFastPathVsSandbox 验证 Fast-Path 与 goja sandbox 对同一算术表达式输出一致。
func TestFastPathVsSandbox(t *testing.T) {
	exprs := []struct {
		expr string
		vars map[string]any
	}{
		{"1+2", nil},
		{"10-4", nil},
		{"3*4", nil},
		{"10/4", nil},
		{"10%3", nil},
		{"(1+2)*3", nil},
		{"1+2*3", nil},
		{"100-50+25", nil},
		{"2*(3+4)", nil},
		{"7/2", nil},
		{"a+b", map[string]any{"a": 3, "b": 4}},
		{"a*b+c", map[string]any{"a": 2, "b": 3, "c": 4}},
	}
	sb := sandbox.New(sandbox.Config{}, nil)
	ctx := context.Background()
	for _, c := range exprs {
		t.Run(c.expr, func(t *testing.T) {
			fast, ok := fastPath(c.expr, c.vars)
			if !ok {
				t.Fatalf("fastPath(%q) unexpectedly rejected", c.expr)
			}
			js, err := sb.Eval(ctx, c.expr, c.vars)
			if err != nil {
				t.Fatalf("sandbox.Eval(%q) error: %v", c.expr, err)
			}
			if fast != js {
				t.Fatalf("fastPath=%q sandbox=%q mismatch", fast, js)
			}
		})
	}
}

// TestFastPathRejects 验证非法表达式回落 sandbox（ok=false）。
func TestFastPathRejects(t *testing.T) {
	exprs := []string{
		"Math.max(1,2)", // 函数调用
		`"a"+"b"`,       // 字符串拼接
		"1?2:3",         // 三元
		"a>b",           // 比较
		"a[0]",          // 下标
		"1++",           // 自增
		"foo()",         // 函数
		"a.b",           // 属性访问
		"[1,2]",         // 数组字面量
		"a===b",         // 严格比较
	}
	for _, expr := range exprs {
		if _, ok := fastPath(expr, nil); ok {
			t.Fatalf("fastPath(%q) should reject, got ok", expr)
		}
	}
}

// TestEvalWithTrace 验证 EvalWithTrace 产生的 StepTrace 各字段，特别是
// Mode 枚举映射（default/css/xpath/json/regex/js/template 对齐 DebugStep.mode）。
func TestEvalWithTrace(t *testing.T) {
	e := newTestEngine()
	ctx := context.Background()

	cases := []struct {
		name   string
		rule   string
		source string
		mode   string
		items  int
	}{
		{"default", "class.content@text", testHTML, "default", 1},
		{"css", "@css:class.content@text", testHTML, "css", 1},
		{"xpath", "//li/text()", testHTML, "xpath", 2},
		{"json", "$.data.name", `{"data":{"name":"斗破苍穹"}}`, "json", 1},
		{"regex", `:(\d+)`, "第120章 风起", "regex", 1},
		{"js", `<js>"["+result+"]"</js>`, "风起", "js", 1},
		{"template", "{{1+2}}", "", "template", 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, steps, err := e.EvalWithTrace(ctx, "content", c.rule, c.source, 0)
			if err != nil {
				t.Fatalf("EvalWithTrace(%q) error: %v", c.rule, err)
			}
			if len(steps) == 0 {
				t.Fatalf("expected at least 1 step, got 0")
			}
			last := steps[len(steps)-1]
			if last.Index != len(steps) {
				t.Fatalf("last step Index=%d, want %d", last.Index, len(steps))
			}
			if last.Phase != "content" {
				t.Fatalf("Phase=%q, want %q", last.Phase, "content")
			}
			if last.Mode != c.mode {
				t.Fatalf("Mode=%q, want %q", last.Mode, c.mode)
			}
			if last.Expr == "" {
				t.Fatalf("Expr is empty")
			}
			if last.InputDigest == "" {
				t.Fatalf("InputDigest is empty")
			}
			if !last.Hit {
				t.Fatalf("Hit=false, want true")
			}
			if last.OutputItems != c.items {
				t.Fatalf("OutputItems=%d, want %d", last.OutputItems, c.items)
			}
			if last.OutputPreview == "" {
				t.Fatalf("OutputPreview is empty")
			}
			if last.ElapsedMs < 0 {
				t.Fatalf("ElapsedMs=%d < 0", last.ElapsedMs)
			}
			if last.ErrorCode != "" {
				t.Fatalf("ErrorCode=%q, want empty on success", last.ErrorCode)
			}
		})
	}
}
