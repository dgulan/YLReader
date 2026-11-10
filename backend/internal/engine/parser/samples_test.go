package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// legadoSample 是真实 Legado 书源样本（仅取本报告需要的字段）。
type legadoSample struct {
	Name string `json:"bookSourceName"`
	URL  string `json:"bookSourceUrl"`
	Rule struct {
		BookList string `json:"bookList"`
		Name     string `json:"name"`
		BookURL  string `json:"bookUrl"`
	} `json:"ruleSearch"`
}

var (
	reTag     = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)`)
	reClass   = regexp.MustCompile(`\.([A-Za-z0-9_-]+)`)
	reID      = regexp.MustCompile(`#([A-Za-z0-9_-]+)`)
	reAttr    = regexp.MustCompile(`\[@?([A-Za-z0-9_:-]+)(?:=["']?([^"'\]]*)["']?)?\]`)
	reIdxDS   = regexp.MustCompile(`\.-?\d+$`)
	reIdxBX   = regexp.MustCompile(`\[-?\d+\]$`)
	reExcl    = regexp.MustCompile(`!\d+$`)
	mockKeyRe = regexp.MustCompile(`^[A-Za-z0-9_$-]+$`)
)

func loadLegadoSamples(t *testing.T) []legadoSample {
	t.Helper()
	dir := filepath.Join("..", "..", "repository", "testdata", "legado_samples")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Skip("legado_samples not found")
	}
	var out []legadoSample
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var arr []legadoSample
		if err := json.Unmarshal(raw, &arr); err != nil {
			continue
		}
		out = append(out, arr...)
	}
	if len(out) == 0 {
		t.Skip("legado_samples empty")
	}
	return out
}

// classifyBookList 判定列表规则模式（报告分层用）。
func classifyBookList(s string) string {
	t := strings.TrimSpace(s)
	t = strings.TrimLeft(t, "+-")
	t = strings.TrimSpace(t)
	low := strings.ToLower(t)
	switch {
	case low == "":
		return "empty"
	case strings.HasPrefix(low, "<js>"), strings.HasPrefix(low, "@js:"):
		return "js"
	case strings.HasPrefix(low, "$."), strings.HasPrefix(low, "$["), strings.HasPrefix(low, "@json:"):
		return "json"
	case strings.HasPrefix(low, "//"), strings.HasPrefix(low, "/html"), strings.HasPrefix(low, "@xpath:"):
		return "xpath"
	default:
		return "css"
	}
}

func pickEven(items []legadoSample, n int) []legadoSample {
	if len(items) <= n {
		return items
	}
	out := make([]legadoSample, 0, n)
	step := float64(len(items)) / float64(n)
	for i := 0; i < n; i++ {
		out = append(out, items[int(float64(i)*step)])
	}
	return out
}

func stripRulePrefix(s string) string {
	t := strings.TrimSpace(s)
	t = strings.TrimLeft(t, "+-")
	t = strings.TrimSpace(t)
	low := strings.ToLower(t)
	switch {
	case strings.HasPrefix(low, "@css:"):
		return strings.TrimSpace(t[5:])
	case strings.HasPrefix(t, "@@"):
		return strings.TrimSpace(t[2:])
	}
	return t
}

// cssEl 是 Mock HTML 的单个简单元素。
type cssEl struct {
	tag     string
	classes []string
	id      string
	attrs   map[string]string
	text    string
}

func parseCSSSeg(seg string) cssEl {
	seg = strings.TrimSpace(seg)
	el := cssEl{tag: "div", attrs: map[string]string{}}
	// 先剥离 Legado 尾部索引 / 排除语法。
	seg = reIdxDS.ReplaceAllString(seg, "")
	seg = reIdxBX.ReplaceAllString(seg, "")
	seg = reExcl.ReplaceAllString(seg, "")
	seg = strings.TrimSpace(seg)
	if seg == "" || seg == "children" {
		return el
	}
	// Legado 简写 class./tag./id./text.
	if i := strings.IndexByte(seg, '.'); i > 0 {
		switch seg[:i] {
		case "class":
			el.classes = append(el.classes, strings.Fields(seg[i+1:])...)
			return el
		case "tag":
			if v := strings.Fields(seg[i+1:]); len(v) > 0 {
				el.tag = v[0]
			}
			return el
		case "id":
			el.id = strings.Fields(seg[i+1:])[0]
			return el
		case "text":
			el.text = seg[i+1:]
			return el
		}
	}
	for _, m := range reAttr.FindAllStringSubmatch(seg, -1) {
		if m[1] != "" {
			el.attrs[m[1]] = m[2]
		}
	}
	// 裸标识符（既非简写也非 HTML 标签）按裸类名处理，如 book_name。
	if bareIdent.MatchString(seg) && !htmlTags[strings.ToLower(seg)] {
		el.classes = append(el.classes, seg)
		return el
	}
	for _, m := range reClass.FindAllStringSubmatch(seg, -1) {
		el.classes = append(el.classes, m[1])
	}
	if m := reID.FindStringSubmatch(seg); m != nil {
		el.id = m[1]
	}
	if m := reTag.FindStringSubmatch(seg); m != nil {
		el.tag = m[1]
	}
	return el
}

func renderCSSEl(el cssEl, inner string) string {
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(el.tag)
	if len(el.classes) > 0 {
		b.WriteString(` class="`)
		b.WriteString(strings.Join(el.classes, " "))
		b.WriteString(`"`)
	}
	if el.id != "" {
		b.WriteString(` id="`)
		b.WriteString(el.id)
		b.WriteString(`"`)
	}
	keys := make([]string, 0, len(el.attrs))
	for k := range el.attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(" ")
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(el.attrs[k])
		b.WriteString(`"`)
	}
	b.WriteString(">")
	if el.text != "" {
		b.WriteString(el.text)
	}
	b.WriteString(inner)
	b.WriteString("</")
	b.WriteString(el.tag)
	b.WriteString(">")
	return b.String()
}

// firstListSegment 取列表规则的首个组合分支（&& / || / %%）。
func firstListSegment(rule string) string {
	r := strings.TrimSpace(rule)
	if segs, _, err := splitRule(r, "&&", "||", "%%"); err == nil && len(segs) > 0 {
		return strings.TrimSpace(segs[0])
	}
	return r
}

// splitSimpleSelectors 把选择器拆成简单选择器：处理 > ~ + 组合符，
// 并保持 `class.top clearfix` 这类多类名简写为同一元素。
func splitSimpleSelectors(s string) []string {
	s = strings.ReplaceAll(s, ">", " ")
	s = strings.ReplaceAll(s, "~", " ")
	s = strings.ReplaceAll(s, "+", " ")
	raw := strings.Fields(s)
	out := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		tok := raw[i]
		if strings.HasPrefix(tok, "class.") {
			merged := tok
			for i+1 < len(raw) && !strings.ContainsAny(raw[i+1], ".#[") {
				merged += " " + raw[i+1]
				i++
			}
			out = append(out, merged)
			continue
		}
		out = append(out, tok)
	}
	return out
}

// cssSink 为字段规则生成可命中的最小子树（每层渲染两个兄弟以支持 .0/.1 索引）。
func cssSink(rule, value string) string {
	body, _, _, _ := splitReplace(strings.TrimSpace(firstListSegment(rule)))
	selector, getter := splitGetter(body)
	if selector == "" {
		selector = body
	}
	var parts []string
	for _, chain := range splitSelectorChain(selector) {
		parts = append(parts, splitSimpleSelectors(chain)...)
	}
	if len(parts) == 0 {
		parts = []string{"div"}
	}
	// 以 tr 开头的字段链：补齐 td 上下文并包 table，避免片段被解析器丢弃。
	wrapped := false
	if p0 := parseCSSSeg(parts[0]); strings.EqualFold(p0.tag, "tr") {
		parts = append([]string{parts[0], "td"}, parts[1:]...)
		wrapped = true
	}
	inner := ""
	for i := len(parts) - 1; i >= 0; i-- {
		el := parseCSSSeg(parts[i])
		if i == len(parts)-1 {
			el.text = "MockTitle"
			if getter == "href" || getter == "src" || el.tag == "a" {
				el.attrs["href"] = value
			}
		}
		one := renderCSSEl(el, inner)
		inner = one + one
	}
	if wrapped {
		inner = "<table><tbody>" + inner + "</tbody></table>"
	}
	return inner
}

// buildCSSMock 依据 bookList 链与字段规则构造最小 Mock HTML。
func buildCSSMock(listRule, nameRule, bookURLRule string) string {
	body, _, _, _ := splitReplace(stripRulePrefix(firstListSegment(listRule)))
	var chain []string
	for _, part := range splitSelectorChain(body) {
		chain = append(chain, splitSimpleSelectors(part)...)
	}
	if len(chain) == 0 {
		chain = []string{"div"}
	}
	sinks := ""
	if strings.TrimSpace(nameRule) != "" {
		sinks += cssSink(nameRule, "http://mock/book/1")
	}
	if strings.TrimSpace(bookURLRule) != "" {
		sinks += cssSink(bookURLRule, "http://mock/book/1")
	}
	outer := sinks
	for i := len(chain) - 1; i >= 0; i-- {
		el := parseCSSSeg(chain[i])
		one := renderCSSEl(el, outer)
		switch strings.ToLower(el.tag) {
		case "tr":
			one = "<table><tbody>" + one + "</tbody></table>"
		case "td", "th":
			one = "<table><tbody><tr>" + one + "</tr></tbody></table>"
		}
		outer = one + one
	}
	return "<html><body>" + outer + "</body></html>"
}

// xpathSeg 是 Mock HTML 的单个 XPath 路径元素。
type xpathSeg struct {
	tag     string
	classes []string
	id      string
	attrs   map[string]string
}

func parseXPathSeg(seg string) (xpathSeg, bool) {
	seg = strings.TrimSpace(seg)
	if seg == "" || seg == "text()" {
		return xpathSeg{}, false
	}
	if strings.HasPrefix(seg, "@") {
		return xpathSeg{}, false
	}
	el := xpathSeg{tag: "div", attrs: map[string]string{}}
	if m := reAttr.FindStringSubmatch(seg); m != nil {
		if m[1] == "class" {
			el.classes = strings.Fields(m[2])
		} else if m[1] == "id" {
			el.id = m[2]
		} else {
			el.attrs[m[1]] = m[2]
		}
	}
	if m := reTag.FindStringSubmatch(seg); m != nil {
		el.tag = m[1]
	}
	if strings.HasPrefix(seg, "*") {
		el.tag = "div"
	}
	// 裸标识符（既非 class/id/属性，也非 HTML 标签）按裸类名处理，
	// 兼容线上大量 `book_name` / `all_book_items` 形态的 Legado 书源。
	if len(el.classes) == 0 && el.id == "" && len(el.attrs) == 0 &&
		!htmlTags[strings.ToLower(el.tag)] && el.tag != "div" && el.tag != "*" {
		el.classes = append(el.classes, el.tag)
		el.tag = "div"
	}
	return el, true
}

func xpathChain(rule string) []xpathSeg {
	r := strings.TrimSpace(rule)
	r = strings.TrimPrefix(r, "@xpath:")
	r = strings.TrimPrefix(r, "@XPath:")
	r = strings.ReplaceAll(r, "//", "/")
	var out []xpathSeg
	for _, tok := range strings.Split(r, "/") {
		if el, ok := parseXPathSeg(tok); ok {
			out = append(out, el)
		}
	}
	return out
}

func renderXPathChain(chain []xpathSeg, inner string) string {
	for i := len(chain) - 1; i >= 0; i-- {
		el := chain[i]
		html := renderCSSEl(cssEl{tag: el.tag, classes: el.classes, id: el.id, attrs: el.attrs}, inner)
		// tr/td/th 必须置于 table 内，否则 HTML 解析会丢弃或重排。
		switch strings.ToLower(el.tag) {
		case "tr":
			html = "<table><tbody>" + html + "</tbody></table>"
		case "td", "th":
			html = "<table><tbody><tr>" + html + "</tr></tbody></table>"
		}
		inner = html
	}
	return inner
}

func buildXPathMock(listRule, nameRule, bookURLRule string) string {
	chain := xpathChain(stripRulePrefix(firstListSegment(listRule)))
	if len(chain) == 0 {
		chain = []xpathSeg{{tag: "div"}}
	}
	sinks := ""
	if strings.TrimSpace(nameRule) != "" {
		sinks += fieldMock(nameRule, "MockTitle")
	}
	if strings.TrimSpace(bookURLRule) != "" {
		sinks += fieldMock(bookURLRule, "http://mock/book/1")
	}
	body := renderXPathChain(chain, sinks)
	return "<html><body>" + body + "</body></html>"
}

// fieldMock 按字段规则的实际模式（CSS/XPath）生成对应形态的子树。
func fieldMock(rule, value string) string {
	if classifyBookList(rule) == "xpath" {
		return xpathSink(rule)
	}
	return cssSink(rule, value)
}

func xpathSink(rule string) string {
	chain := xpathChain(rule)
	if len(chain) == 0 {
		return ""
	}
	inner := ""
	for i := len(chain) - 1; i >= 0; i-- {
		el := chain[i]
		attrs := map[string]string{}
		for k, v := range el.attrs {
			attrs[k] = v
		}
		text := ""
		if i == len(chain)-1 {
			text = "MockTitle"
		}
		if el.tag == "a" {
			attrs["href"] = "http://mock/book/1"
		}
		inner = renderCSSEl(cssEl{tag: el.tag, classes: el.classes, id: el.id, attrs: attrs, text: text}, inner)
		inner = inner + inner // 两个兄弟，覆盖 [2] / position() 等谓词
	}
	return inner
}

// jsonPathOf 把规则收敛成点分路径（供 Mock JSON 构造）。
func jsonPathOf(rule string) string {
	t := strings.TrimSpace(rule)
	t = strings.TrimLeft(t, "+-")
	t = strings.TrimSpace(t)
	low := strings.ToLower(t)
	if strings.HasPrefix(low, "@json:") {
		t = strings.TrimSpace(t[6:])
	}
	// `##` 之后是正则替换段，不属于 JSON 路径。
	if i := strings.Index(t, "##"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	if strings.HasPrefix(t, "$.") {
		t = t[2:]
	} else if strings.HasPrefix(t, "$") {
		t = t[1:]
	}
	if i := strings.Index(t, "||"); i >= 0 {
		t = t[:i]
	}
	t = strings.ReplaceAll(t, "[*]", "")
	t = strings.ReplaceAll(t, "..", ".")
	t = strings.Trim(t, ".")
	t = strings.ReplaceAll(t, "[", ".")
	t = strings.ReplaceAll(t, "]", "")
	return t
}

var templateKeyRe = regexp.MustCompile(`\{\{\s*\$\.([A-Za-z0-9_]+)\s*\}\}`)

// templateKeys 提取字段规则中 `{{$.key}}` 引用的 JSON 键（Mock 数据需包含）。
func templateKeys(rule string) []string {
	var out []string
	for _, m := range templateKeyRe.FindAllStringSubmatch(rule, -1) {
		out = append(out, m[1])
	}
	return out
}

func buildJSONMock(listRule, nameRule, bookURLRule string) string {
	path := jsonPathOf(listRule)
	segs := []string{}
	for _, s := range strings.Split(path, ".") {
		if s = strings.TrimSpace(s); s != "" {
			segs = append(segs, s)
		}
	}
	item := map[string]any{
		"name": "MockTitle", "title": "MockTitle", "bookName": "MockTitle",
		"author": "MockAuthor", "bookUrl": "http://mock/book/1", "url": "http://mock/book/1",
		"coverUrl": "http://mock/img/1.jpg", "intro": "MockIntro", "kind": "MockKind",
		"lastChapter": "第1章", "wordCount": 1000,
	}
	addPath := func(rule, value string) {
		p := jsonPathOf(rule)
		if p == "" {
			return
		}
		parts := []string{}
		for _, s := range strings.Split(p, ".") {
			if s = strings.TrimSpace(s); s != "" {
				// 只接受简单键名，避免把 @put/URL/模板等非路径文本塞进 Mock。
				if !mockKeyRe.MatchString(s) {
					return
				}
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return
		}
		cur := item
		for _, s := range parts[:len(parts)-1] {
			next, ok := cur[s].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[s] = next
			}
			cur = next
		}
		cur[parts[len(parts)-1]] = value
	}
	if strings.TrimSpace(nameRule) != "" {
		addPath(nameRule, "MockTitle")
		for _, key := range templateKeys(nameRule) {
			addPath(key, "MockTitle")
		}
	}
	if strings.TrimSpace(bookURLRule) != "" {
		addPath(bookURLRule, "http://mock/book/1")
		for _, key := range templateKeys(bookURLRule) {
			addPath(key, "http://mock/book/1")
		}
	}

	var cur any = []any{item}
	for i := len(segs) - 1; i >= 0; i-- {
		cur = map[string]any{segs[i]: cur}
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func mockContent(cat string, s legadoSample) string {
	switch cat {
	case "json":
		return buildJSONMock(s.Rule.BookList, s.Rule.Name, s.Rule.BookURL)
	case "xpath":
		return buildXPathMock(s.Rule.BookList, s.Rule.Name, s.Rule.BookURL)
	default:
		return buildCSSMock(s.Rule.BookList, s.Rule.Name, s.Rule.BookURL)
	}
}

// evaluateSample 执行「列表切分 + name + bookUrl」三条判据。
func evaluateSample(ctx context.Context, e *Engine, s legadoSample, content string) (bool, string) {
	nodes, err := e.EvalListNodes(ctx, s.Rule.BookList, content)
	if err != nil {
		return false, "bookList 错误: " + err.Error()
	}
	if len(nodes) == 0 {
		return false, "bookList 未切分出节点"
	}
	e.ResetVars()
	name, err := e.EvalField(ctx, nodes[0], s.Rule.Name)
	if err != nil {
		return false, "name 错误: " + err.Error()
	}
	if strings.TrimSpace(name) == "" {
		return false, "name 为空"
	}
	e.ResetVars()
	bookURL, err := e.EvalField(ctx, nodes[0], s.Rule.BookURL)
	if err != nil {
		return false, "bookUrl 错误: " + err.Error()
	}
	bookURL = strings.TrimSpace(bookURL)
	if bookURL == "" {
		return false, "bookUrl 为空"
	}
	if _, err := url.Parse(bookURL); err != nil {
		return false, "bookUrl 无法解析: " + bookURL
	}
	return true, ""
}

// hasRecursiveDescent 判断规则是否使用 Jayway 递归下降 `..`。M1c 收紧后该语法
// 显式返回 unsupported_rule，静态检查将其计入「不支持」而非通过/失败。
func hasRecursiveDescent(bookList, name, bookURL string) bool {
	return strings.Contains(bookList, "..") || strings.Contains(name, "..") || strings.Contains(bookURL, "..")
}

// TestLegadoSyntaxEvaluateRate 对 50 条真实书源做静态语法可求值率检查。
// 语义边界：基于书源自身规则动态构造最小 Mock 数据，仅验证规则语法可求值，
// 不代表真实站点端到端抓取（该对拍由 M2 承接）。
func TestLegadoSyntaxEvaluateRate(t *testing.T) {
	sources := loadLegadoSamples(t)

	var css, jsonS, xpath []legadoSample
	for _, s := range sources {
		if strings.TrimSpace(s.Rule.BookList) == "" ||
			strings.TrimSpace(s.Rule.Name) == "" ||
			strings.TrimSpace(s.Rule.BookURL) == "" {
			continue
		}
		switch classifyBookList(s.Rule.BookList) {
		case "css":
			css = append(css, s)
		case "json":
			jsonS = append(jsonS, s)
		case "xpath":
			xpath = append(xpath, s)
		}
	}

	picks := make([]legadoSample, 0, 50)
	picks = append(picks, pickEven(css, 25)...)
	picks = append(picks, pickEven(jsonS, 15)...)
	picks = append(picks, pickEven(xpath, 10)...)
	if len(picks) == 0 {
		t.Skip("no eligible samples")
	}

	ctx := context.Background()
	e := newTestEngine()

	type row struct {
		name        string
		url         string
		cat         string
		list        string
		nameR       string
		bookR       string
		ok          bool
		unsupported bool
		reason      string
	}
	rows := make([]row, 0, len(picks))
	pass, unsupportedCount := 0, 0
	byCat := map[string][2]int{}
	for _, s := range picks {
		cat := classifyBookList(s.Rule.BookList)
		content := mockContent(cat, s)
		r := row{
			name: s.Name, url: s.URL, cat: cat,
			list: s.Rule.BookList, nameR: s.Rule.Name, bookR: s.Rule.BookURL,
		}
		if hasRecursiveDescent(s.Rule.BookList, s.Rule.Name, s.Rule.BookURL) {
			// `..` 自 M1c 收紧起显式拒绝（unsupported_rule），不计入可求值率分母。
			unsupportedCount++
			r.unsupported = true
			r.reason = "递归下降 '..' 显式不支持（unsupported_rule）"
			rows = append(rows, r)
			continue
		}
		ok, reason := evaluateSample(ctx, e, s, content)
		r.ok, r.reason = ok, reason
		if ok {
			pass++
		}
		c := byCat[cat]
		c[1]++
		if ok {
			c[0]++
		}
		byCat[cat] = c
		rows = append(rows, r)
	}

	supported := len(rows) - unsupportedCount
	rate := 0.0
	if supported > 0 {
		rate = float64(pass) * 100 / float64(supported)
	}
	if rate < 90 {
		t.Errorf("可求值率 %.1f%% < 90%% (%d/%d, 另 %d 条显式不支持的递归下降未计入)", rate, pass, supported, unsupportedCount)
	}
	t.Logf("可求值率: %.1f%% (%d/%d)，显式不支持 %d 条", rate, pass, supported, unsupportedCount)
	catOrder := []string{"css", "json", "xpath"}
	for _, c := range catOrder {
		if v, ok := byCat[c]; ok {
			t.Logf("  %s: %d/%d", c, v[0], v[1])
		}
	}

	if p := os.Getenv("M1C_REPORT"); p != "" {
		var b strings.Builder
		fmt.Fprintf(&b, "# M1c 规则语法可求值率报告（静态检查）\n\n")
		fmt.Fprintf(&b, "> 文档性质：**静态语法检查**。按每条书源的 `ruleSearch.bookList` / `name` / `bookUrl` 规则动态构造最小 Mock 数据，验证「列表可切分、name 非空、bookUrl 非空且可解析」。**不代表真实站点端到端抓取**；真实多源对拍由 M2 承接。\n\n")
		fmt.Fprintf(&b, "- 样本来源：`backend/internal/repository/testdata/legado_samples/`（3bb7b751.json 2117 条、b778fe6b.json 3907 条，共 4621 条真实书源）。\n")
		fmt.Fprintf(&b, "- 分层抽样：CSS 25、JSON 15、XPath 10（JS 与空 bookList 源不在三类目标范围内，不计入分母）。\n")
		fmt.Fprintf(&b, "- 判据：① `EvalListNodes` 返回节点数 ≥ 1；② 首个节点 `name` 非空；③ 首个节点 `bookUrl` 非空且 `url.Parse` 通过。\n")
		fmt.Fprintf(&b, "- 递归下降：`..` 在 gjson 中无等价语义，M1c 起显式返回 `unsupported_rule`；此类样本单列，不计入可求值率分母。\n\n")
		fmt.Fprintf(&b, "## 结论\n\n")
		fmt.Fprintf(&b, "- **可求值率：%.1f%%（%d/%d）**，目标 ≥ 90%%：%s\n", rate, pass, supported, map[bool]string{true: "PASS", false: "FAIL"}[rate >= 90])
		fmt.Fprintf(&b, "- 显式不支持的递归下降样本：%d 条（未计入分母）\n", unsupportedCount)
		for _, c := range catOrder {
			if v, ok := byCat[c]; ok {
				fmt.Fprintf(&b, "- %s：%d/%d\n", c, v[0], v[1])
			}
		}
		fmt.Fprintf(&b, "\n## 明细\n\n| # | 模式 | 结果 | 书源 | bookList | name | bookUrl | 原因 |\n|---|---|---|---|---|---|---|---|\n")
		for i, r := range rows {
			res := "PASS"
			if r.unsupported {
				res = "UNSUPPORTED"
			} else if !r.ok {
				res = "FAIL"
			}
			esc := func(s string) string {
				return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
			}
			fmt.Fprintf(&b, "| %d | %s | %s | %s | `%s` | `%s` | `%s` | %s |\n",
				i+1, r.cat, res, esc(r.name), esc(r.list), esc(r.nameR), esc(r.bookR), esc(r.reason))
		}
		fmt.Fprintf(&b, "\n## 未通过样本的语法原因\n\n")
		any := false
		for _, r := range rows {
			if !r.ok && !r.unsupported {
				any = true
				fmt.Fprintf(&b, "- **%s**（%s）：%s\n", r.name, r.cat, r.reason)
			}
		}
		if !any {
			fmt.Fprintf(&b, "无。\n")
		}
		if unsupportedCount > 0 {
			fmt.Fprintf(&b, "\n## 显式不支持的递归下降样本（不计入分母）\n\n")
			for _, r := range rows {
				if r.unsupported {
					fmt.Fprintf(&b, "- **%s**（%s）：`%s`\n", r.name, r.cat, strings.ReplaceAll(r.list, "|", "\\|"))
				}
			}
		}
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("report written to %s", p)
	}
}
