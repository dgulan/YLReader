package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/antchfx/htmlquery"
	"github.com/tidwall/gjson"
	"golang.org/x/net/html"

	"yreader/backend/internal/engine/sandbox"
)

// ResetVars 清空当前引擎的局部变量（@put/@get 变量池）。
// 作用域契约：必须在每个 item 循环开始时调用一次，同一 item 内各字段共享，
// 严禁在单个字段求值前后调用。
func (e *Engine) ResetVars() {
	e.vars = map[string]any{}
}

// EvalListNodes 按单条列表规则切分内容，产出结构化节点列表。
// 直接复用 detectMode 做模式识别（支持 @@、/、@XPath:、@Json:、$.、$[、: regex）。
// 若规则含前置 JS 段落（<js>...</js> / @js:），先执行 JS 变换内容，再对最后
// 一段做列表切分。
func (e *Engine) EvalListNodes(ctx context.Context, listRule, content string) ([]RuleNode, error) {
	listRule = strings.TrimSpace(listRule)
	if listRule == "" {
		return []RuleNode{}, nil
	}

	segs := splitSourceRule(listRule)
	if len(segs) == 0 {
		return []RuleNode{}, nil
	}
	// 前置段落作为内容变换依次执行。
	for i := 0; i < len(segs)-1; i++ {
		out, err := e.evalSegmentAsContent(ctx, segs[i], content)
		if err != nil {
			return nil, err
		}
		content = out
	}

	last := segs[len(segs)-1]
	if last.mode == ModeJS {
		v, err := e.evalJSValue(ctx, last.raw, content)
		if err != nil {
			return nil, err
		}
		return nodesFromJSValue(v)
	}

	mode, rule := detectMode(last.raw)
	switch mode {
	case ModeXPath:
		return evalListNodesXPath(rule, content)
	case ModeJSON:
		return evalListNodesJSON(rule, content)
	case ModeRegex:
		return evalListNodesRegex(rule, content)
	case ModeJS:
		v, err := e.evalJSValue(ctx, rule, content)
		if err != nil {
			return nil, err
		}
		return nodesFromJSValue(v)
	default:
		return evalListNodesCSS(rule, content)
	}
}

// evalSegmentAsContent 把一个前置段落当作内容变换执行。
func (e *Engine) evalSegmentAsContent(ctx context.Context, seg sourceRule, content string) (string, error) {
	if seg.mode == ModeJS {
		return e.evalJSBody(ctx, seg.raw, content)
	}
	return e.Eval(ctx, seg.raw, content)
}

// evalJSBody 直接执行一段 JS（raw 为脚本体，无 <js> 包裹）。
func (e *Engine) evalJSBody(ctx context.Context, script, content string) (string, error) {
	if e.sb == nil {
		return "", sandbox.ErrRuleExecution
	}
	vars := map[string]any{"result": content}
	for k, v := range e.vars {
		vars[k] = v
	}
	return e.sb.Eval(ctx, script, vars)
}

// evalJSValue 执行 JS 并返回导出后的 Go 值（用于数组/对象切分）。
func (e *Engine) evalJSValue(ctx context.Context, script, content string) (any, error) {
	if e.sb == nil {
		return nil, sandbox.ErrRuleExecution
	}
	vars := map[string]any{"result": content}
	for k, v := range e.vars {
		vars[k] = v
	}
	return e.sb.EvalValue(ctx, script, vars)
}

// nodesFromJSValue 把 JS 导出值包装为节点列表：数组逐项，对象→NodeJSON，
// 标量→NodeText，null/undefined→空列表。
func nodesFromJSValue(v any) ([]RuleNode, error) {
	switch vv := v.(type) {
	case nil:
		return []RuleNode{}, nil
	case []any:
		out := make([]RuleNode, 0, len(vv))
		for _, item := range vv {
			n, err := nodeFromJSElement(item)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	default:
		n, err := nodeFromJSElement(vv)
		if err != nil {
			return nil, err
		}
		return []RuleNode{n}, nil
	}
}

func nodeFromJSElement(v any) (RuleNode, error) {
	switch vv := v.(type) {
	case map[string]any, []any:
		raw, err := json.Marshal(vv)
		if err != nil {
			return RuleNode{}, err
		}
		return RuleNode{Kind: NodeJSON, raw: raw}, nil
	case nil:
		return RuleNode{Kind: NodeText, text: ""}, nil
	case string:
		return RuleNode{Kind: NodeText, text: vv}, nil
	default:
		return RuleNode{Kind: NodeText, text: fmt.Sprint(vv)}, nil
	}
}

// evalListNodesCSS 用 goquery 按选择器链切分列表，支持 && / || / %% 组合。
func evalListNodesCSS(rule, content string) ([]RuleNode, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	segs, op, err := splitRule(strings.TrimSpace(rule), "&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	out := make([]RuleNode, 0, 16)
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		sel := doc.Selection
		for _, part := range splitSelectorChain(seg) {
			sel = selectElements(sel, part)
		}
		before := len(out)
		seen := map[*html.Node]bool{}
		for _, n := range sel.Nodes {
			if n == nil || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, RuleNode{Kind: NodeHTML, docNode: n})
		}
		if op == "||" && len(out) > before {
			break
		}
	}
	return out, nil
}

// evalListNodesXPath 用 htmlquery 切分列表。
func evalListNodesXPath(rule, content string) ([]RuleNode, error) {
	doc, err := htmlquery.Parse(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	segs, op, err := splitRule(strings.TrimSpace(rule), "&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	var out []RuleNode
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		nodes := htmlquery.Find(doc, seg)
		for _, n := range nodes {
			out = append(out, RuleNode{Kind: NodeHTML, docNode: n})
		}
		if op == "||" && len(out) > 0 {
			break
		}
	}
	return out, nil
}

// evalListNodesJSON 用 gjson 切分 JSON 数组。
func evalListNodesJSON(rule, content string) ([]RuleNode, error) {
	rule = strings.TrimSpace(rule)
	// 递归下降 `..` 显式拒绝（gjson 无等价语义，静默降级不可诊断）。
	if strings.Contains(rule, "..") {
		return nil, fmt.Errorf("%w: recursive descent '..' is not supported", sandbox.ErrUnsupportedRule)
	}
	segs, op, err := splitRule(rule, "&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	var out []RuleNode
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		res := gjson.Get(content, toGJSONPath(seg))
		if !res.Exists() || res.Type == gjson.Null {
			continue
		}
		if res.IsArray() {
			for _, item := range res.Array() {
				out = append(out, RuleNode{Kind: NodeJSON, raw: json.RawMessage(item.Raw)})
			}
		} else {
			out = append(out, RuleNode{Kind: NodeJSON, raw: json.RawMessage(res.Raw)})
		}
		if op == "||" && len(out) > 0 {
			break
		}
	}
	return out, nil
}

// evalListNodesRegex 用正则切分列表，每个匹配结果包装为 NodeText。
func evalListNodesRegex(rule, content string) ([]RuleNode, error) {
	re, err := regexp.Compile(rule)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRuleSyntax, err)
	}
	matches := re.FindAllStringSubmatch(content, -1)
	out := make([]RuleNode, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, RuleNode{Kind: NodeText, text: m[1]})
		} else {
			out = append(out, RuleNode{Kind: NodeText, text: m[0]})
		}
	}
	return out, nil
}

// EvalField 在单个节点上求值字段规则。
// 复用 M1a 的 EvalList 全套管线（|| / && / %% / ## / makeUp / @put / @get）；
// 仅对 CSS 的显式 @getter 与 JSON 的纯 @js: 做必要的预处理。
func (e *Engine) EvalField(ctx context.Context, node RuleNode, rule string) (string, error) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return "", nil
	}

	mode, _ := detectFieldMode(rule)
	// （1）跨类型不兼容预检。
	switch node.Kind {
	case NodeHTML:
		if mode == ModeJSON {
			return "", sandbox.ErrUnsupportedRule
		}
	case NodeJSON:
		if mode == ModeXPath || strings.HasPrefix(rule, "@css:") || strings.HasPrefix(rule, "@@") {
			return "", sandbox.ErrUnsupportedRule
		}
	case NodeText:
		if mode == ModeXPath || mode == ModeJSON || strings.HasPrefix(rule, "@css:") || strings.HasPrefix(rule, "@@") {
			return "", sandbox.ErrUnsupportedRule
		}
	}

	// （2）NodeJSON 无前缀的简单路径规则自动补 $.。
	if node.Kind == NodeJSON && mode == ModeDefault && jsonPlainPath.MatchString(rule) {
		rule = "$." + rule
		mode, _ = detectMode(rule)
	}

	// NodeJSON 的纯 @js: 规则：把解析后的对象注入 result，字段可直接
	// result.fieldName；混合规则（.x@js:...）走标准管线。
	if node.Kind == NodeJSON && mode == ModeJS {
		var val any
		if err := json.Unmarshal(node.raw, &val); err == nil {
			prev, had := e.vars["result"]
			e.vars["result"] = val
			defer func() {
				if had {
					e.vars["result"] = prev
				} else {
					delete(e.vars, "result")
				}
			}()
		}
	}

	// （3）CSS 显式 @getter：提前提取并返回，避免 @href 被当作选择器。
	// 仅对「非 @ 前缀、非模板、非 @put/@get」的默认模式规则生效。
	if mode == ModeDefault && !strings.HasPrefix(rule, "@") && !strings.Contains(rule, "{{") &&
		!strings.Contains(rule, "@put:") && !strings.Contains(rule, "@get:") &&
		!strings.Contains(rule, "||") && !strings.Contains(rule, "&&") && !strings.Contains(rule, "%%") {
		body, repl, replacement, first := splitReplace(rule)
		selector, getter := splitGetter(body)
		switch {
		case getter == "" && selector != "":
			// 无 getter → 默认取文本：补 @text 复用 M1a 的 evalCSS。
			list, err := e.EvalList(ctx, body+"@text", node.Raw())
			if err != nil {
				return "", err
			}
			val := firstNonEmpty(list)
			if repl != "" {
				if out := applyReplace([]string{val}, repl, replacement, first); len(out) > 0 {
					val = out[0]
				}
			}
			return strings.TrimSpace(val), nil
		case getter != "" && getter != "text" && selector != "" && simpleAttrRe.MatchString(getter):
			val := extractGetter(node, selector, getter)
			if repl != "" {
				if out := applyReplace([]string{val}, repl, replacement, first); len(out) > 0 {
					val = out[0]
				}
			}
			return strings.TrimSpace(val), nil
		}
	}

	// （4）标准管线。
	list, err := e.EvalList(ctx, rule, node.Raw())
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s), nil
		}
	}
	return "", nil
}

// jsonPlainPath 匹配可安全补 `$.` 前缀的简单路径规则。
var jsonPlainPath = regexp.MustCompile(`^[A-Za-z0-9_.\[\]-]+$`)

// simpleAttrRe 匹配「简单属性名」getter；带 : / ( 等复杂 getter（如 @js:）必须走标准管线。
var simpleAttrRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// extractGetter 在节点上按 selector 查找子节点并提取 getter 值。
func extractGetter(node RuleNode, selector, getter string) string {
	if node.Kind != NodeHTML || node.docNode == nil {
		return ""
	}
	if selector == "" {
		if v := node.Attr(getter); v != "" {
			return v
		}
		return ""
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(node.OuterHTML()))
	if err != nil {
		return ""
	}
	sel := doc.Selection
	for _, part := range splitSelectorChain(selector) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sel = selectElements(sel, part)
	}
	if sel.Length() == 0 {
		return ""
	}
	first := sel.First()
	switch getter {
	case "textNodes", "ownText", "html", "all":
		if out := getResultLast(sel, getter); len(out) > 0 {
			return out[0]
		}
		return ""
	default:
		if v, ok := first.Attr(getter); ok {
			return v
		}
		return ""
	}
}

// firstNonEmpty 返回列表中首个非空串。
func firstNonEmpty(list []string) string {
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// detectFieldMode 在 detectMode 基础上补上 `@js:` / `<js>` 前缀识别
// （Legado 的 JS 规则由 splitSourceRule 切分，detectMode 本身不覆盖）。
func detectFieldMode(rule string) (Mode, string) {
	trimmed := strings.TrimSpace(rule)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "@js:") {
		return ModeJS, jsScriptBody(trimmed)
	}
	if strings.HasPrefix(lower, "<js>") {
		return ModeJS, jsScriptBody(trimmed)
	}
	return detectMode(trimmed)
}

// jsScriptBody 从 `@js:xxx` / `<js>xxx</js>` 中提取脚本体；其它形式原样返回。
func jsScriptBody(rule string) string {
	lower := strings.ToLower(rule)
	if strings.HasPrefix(lower, "@js:") {
		return strings.TrimSpace(rule[len("@js:"):])
	}
	if strings.HasPrefix(lower, "<js>") && strings.HasSuffix(lower, "</js>") {
		return strings.TrimSpace(rule[len("<js>") : len(rule)-len("</js>")])
	}
	return rule
}
