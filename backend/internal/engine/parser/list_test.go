package parser

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"yreader/backend/internal/engine/sandbox"
)

const listHTML = `<html><body>
<div class="book"><a class="title" href="/book/1">书名一</a><span class="author">作者一</span></div>
<div class="book"><a class="title" href="/book/2">书名二</a><span class="author">作者二</span></div>
</body></html>`

const listJSON = `{"data":{"posts":[{"name":"A","url":"/1"},{"name":"B","url":"/2"}]}}`

func TestEvalListNodesCSS(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()

	nodes, err := e.EvalListNodes(ctx, ".book", listHTML)
	if err != nil {
		t.Fatalf("EvalListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if got := nodes[0].Text(); !strings.Contains(got, "书名一") {
		t.Fatalf("node0 text = %q", got)
	}

	// 「[*]」在部分书源中表示全部元素，应等价于无索引。
	nodes, err = e.EvalListNodes(ctx, ".book[*]", listHTML)
	if err != nil {
		t.Fatalf("EvalListNodes [*]: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes for [*], got %d", len(nodes))
	}

	// Legado 链式选择器：class.book@tag.a
	nodes, err = e.EvalListNodes(ctx, "class.book@tag.a", listHTML)
	if err != nil {
		t.Fatalf("EvalListNodes chain: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes for chain, got %d", len(nodes))
	}
	if got := nodes[0].Text(); got != "书名一" {
		t.Fatalf("chain node0 text = %q", got)
	}
	if got := nodes[0].Attr("href"); got != "/book/1" {
		t.Fatalf("chain node0 href = %q", got)
	}
}

func TestEvalListNodesXPath(t *testing.T) {
	e := newTestEngine()
	nodes, err := e.EvalListNodes(context.Background(), `//div[@class="book"]`, listHTML)
	if err != nil {
		t.Fatalf("EvalListNodes xpath: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
}

func TestEvalListNodesJSON(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()

	nodes, err := e.EvalListNodes(ctx, "$.data.posts[*]", listJSON)
	if err != nil {
		t.Fatalf("EvalListNodes json: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Kind != NodeJSON {
		t.Fatalf("want NodeJSON, got %v", nodes[0].Kind)
	}
	if got, _ := e.EvalField(ctx, nodes[0], "name"); got != "A" {
		t.Fatalf("field name = %q", got)
	}
	if got, _ := e.EvalField(ctx, nodes[1], "$.url"); got != "/2" {
		t.Fatalf("field url = %q", got)
	}
}

func TestEvalListNodesRegex(t *testing.T) {
	e := newTestEngine()
	nodes, err := e.EvalListNodes(context.Background(), `:(\w)\d`, "a1 b2 c3")
	if err != nil {
		t.Fatalf("EvalListNodes regex: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(nodes))
	}
	if nodes[0].text != "a" || nodes[1].text != "b" || nodes[2].text != "c" {
		t.Fatalf("regex nodes = %q,%q,%q", nodes[0].text, nodes[1].text, nodes[2].text)
	}
}

func TestEvalFieldCSSGetter(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()
	nodes, err := e.EvalListNodes(ctx, ".book", listHTML)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("EvalListNodes: %v, %d", err, len(nodes))
	}
	node := nodes[0]

	cases := []struct {
		rule string
		want string
	}{
		{"a@href", "/book/1"},
		{".title@text", "书名一"},
		{".author", "作者一"},
		{"class.title@text", "书名一"},
		{".title@href", "/book/1"},
	}
	for _, c := range cases {
		got, err := e.EvalField(ctx, node, c.rule)
		if err != nil {
			t.Fatalf("EvalField(%q): %v", c.rule, err)
		}
		if got != c.want {
			t.Fatalf("EvalField(%q) = %q, want %q", c.rule, got, c.want)
		}
	}
}

func TestSplitGetterQuoted(t *testing.T) {
	cases := []struct {
		rule     string
		selector string
		getter   string
	}{
		{`div[data-x="a@b"]@text`, `div[data-x="a@b"]`, "text"},
		{`a@href`, "a", "href"},
		{`div.title`, "div.title", ""},
		{`ul@li!0`, "ul", "li!0"},
	}
	for _, c := range cases {
		sel, get := splitGetter(c.rule)
		if sel != c.selector || get != c.getter {
			t.Fatalf("splitGetter(%q) = (%q,%q), want (%q,%q)", c.rule, sel, get, c.selector, c.getter)
		}
	}
}

func TestEvalFieldCrossType(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()

	htmlNodes, err := e.EvalListNodes(ctx, ".book", listHTML)
	if err != nil || len(htmlNodes) == 0 {
		t.Fatalf("EvalListNodes: %v", err)
	}
	if _, err := e.EvalField(ctx, htmlNodes[0], `@json:$.name`); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatalf("html+json err = %v", err)
	}

	jsonNodes, err := e.EvalListNodes(ctx, "$.data.posts[*]", listJSON)
	if err != nil || len(jsonNodes) == 0 {
		t.Fatalf("EvalListNodes json: %v", err)
	}
	if _, err := e.EvalField(ctx, jsonNodes[0], `@css:.name`); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatalf("json+css err = %v", err)
	}

	text := RuleNode{Kind: NodeText, text: "hello"}
	if _, err := e.EvalField(ctx, text, `@xpath://div`); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatalf("text+xpath err = %v", err)
	}
}

func TestEvalFieldPutGetScope(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()

	item := `<div class="book"><a href="/book/9">书名九</a></div>`
	nodes, err := e.EvalListNodes(ctx, ".book", item)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("EvalListNodes: %v, %d", err, len(nodes))
	}
	node := nodes[0]

	// item 内 name 规则写入 @put:{u:...}，后续字段用 @get:{u} 读取。
	got, err := e.EvalField(ctx, node, `@put:{u:a@href}a@text`)
	if err != nil {
		t.Fatalf("put field: %v", err)
	}
	if got != "书名九" {
		t.Fatalf("put field = %q", got)
	}
	got, err = e.EvalField(ctx, node, `@get:{u}`)
	if err != nil {
		t.Fatalf("get field: %v", err)
	}
	if got != "/book/9" {
		t.Fatalf("get field = %q", got)
	}

	// 下一个 item 开始时 ResetVars 必须清空，杜绝跨 item 串扰。
	// 注：变量缺失时 @get 回退为空规则，Legado 语义下空规则透传当前内容，
	// 因此断言「不再拿到上一个 item 的变量值」。
	e.ResetVars()
	got, err = e.EvalField(ctx, node, `@get:{u}`)
	if err != nil {
		t.Fatalf("get after reset: %v", err)
	}
	if got == "/book/9" {
		t.Fatalf("get after reset still returns previous item's value %q", got)
	}
}

func TestEvalFieldJSONJS(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()
	nodes, err := e.EvalListNodes(ctx, "$.data.posts[*]", listJSON)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("EvalListNodes: %v", err)
	}
	got, err := e.EvalField(ctx, nodes[0], `@js:result.name + '|' + result.url`)
	if err != nil {
		t.Fatalf("json js field: %v", err)
	}
	if got != "A|/1" {
		t.Fatalf("json js field = %q", got)
	}
}

func TestRuleNodeAttrEqualFold(t *testing.T) {
	n := &html.Node{
		Type: html.ElementNode,
		Data: "a",
		Attr: []html.Attribute{{Key: "HREF", Val: "http://example.com/x"}},
	}
	node := RuleNode{Kind: NodeHTML, docNode: n}
	if got := node.Attr("href"); got != "http://example.com/x" {
		t.Fatalf("Attr(href) = %q", got)
	}
	if got := node.Attr("Href"); got != "http://example.com/x" {
		t.Fatalf("Attr(Href) = %q", got)
	}
}

func TestEvalNextURL(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()

	cssHTML := `<html><body><a class="next" href="/page/2">下一页</a></body></html>`
	got, err := e.EvalNextURL(ctx, ".next@href", cssHTML)
	if err != nil || got != "/page/2" {
		t.Fatalf("css next = %q, %v", got, err)
	}

	got, err = e.EvalNextURL(ctx, "$.next", `{"next":"/p2"}`)
	if err != nil || got != "/p2" {
		t.Fatalf("json string next = %q, %v", got, err)
	}

	got, err = e.EvalNextURL(ctx, "$.page", `{"page":2}`)
	if err != nil || got != "2" {
		t.Fatalf("json number next = %q, %v", got, err)
	}

	got, err = e.EvalNextURL(ctx, "//a[@class='next']/@href", cssHTML)
	if err != nil || got != "/page/2" {
		t.Fatalf("xpath next = %q, %v", got, err)
	}

	got, err = e.EvalNextURL(ctx, "@js:result.match(/href=\"([^\"]+)\"/)[1]", cssHTML)
	if err != nil || got != "/page/2" {
		t.Fatalf("js next = %q, %v", got, err)
	}
}

// TestRecursiveDescentRejected 递归下降 `..` 在 gjson 中无等价语义，必须显式
// 报 ErrUnsupportedRule（上层归 unsupported_rule），不得静默降级为子路径匹配。
func TestRecursiveDescentRejected(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine()

	if _, err := e.EvalListNodes(ctx, "$.data..posts", listJSON); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatalf("EvalListNodes $.. err = %v", err)
	}

	nodes, err := e.EvalListNodes(ctx, "$.data.posts[*]", listJSON)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("EvalListNodes json: %v", err)
	}
	if _, err := e.EvalField(ctx, nodes[0], `@json:$..name`); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatalf("EvalField $.. err = %v", err)
	}

	if _, err := e.EvalNextURL(ctx, "$..next", `{"a":{"next":"/p2"}}`); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatalf("EvalNextURL $.. err = %v", err)
	}

	// 经典规则 `@json:$..data` 走顶层 Eval 也必须报错，而不是返回空串。
	if _, err := e.Eval(ctx, `@json:$..name`, listJSON); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatalf("Eval $.. err = %v", err)
	}
}
