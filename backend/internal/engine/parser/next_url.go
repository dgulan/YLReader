package parser

import (
	"context"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/tidwall/gjson"

	"yreader/backend/internal/engine/sandbox"
)

// EvalNextURL 从当前内容中按 nextRule 提取下一页 URL（相对或绝对）。
// 纯函数契约：不做绝对化、不发起任何网络 I/O；翻页与 ResolveReference 由
// Service 层负责。结果为空时返回 ("", nil)。
func (e *Engine) EvalNextURL(ctx context.Context, nextRule, content string) (string, error) {
	nextRule = strings.TrimSpace(nextRule)
	if nextRule == "" {
		return "", nil
	}
	mode, rule := detectFieldMode(nextRule)
	switch mode {
	case ModeJS:
		out, err := e.evalJSBody(ctx, jsScriptBody(nextRule), content)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	case ModeJSON:
		// 递归下降 `..` 显式拒绝（evalNextURLJSON 无 error 返回，故在此前置校验）。
		if strings.Contains(rule, "..") {
			return "", fmt.Errorf("%w: recursive descent '..' is not supported", sandbox.ErrUnsupportedRule)
		}
		return evalNextURLJSON(rule, content), nil
	case ModeXPath:
		list, err := evalXPath(rule, content)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(firstNonEmpty(list)), nil
	case ModeRegex:
		list, err := evalListNodesRegex(rule, content)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "", nil
		}
		return strings.TrimSpace(list[0].text), nil
	default:
		return evalNextURLCSS(rule, content)
	}
}

// evalNextURLJSON 用 gjson 提取下一页 URL；数字/布尔结果转字符串。
func evalNextURLJSON(rule, content string) string {
	res := gjson.Get(content, toGJSONPath(rule))
	if !res.Exists() || res.Type == gjson.Null {
		return ""
	}
	switch res.Type {
	case gjson.String:
		return strings.TrimSpace(res.String())
	case gjson.Number, gjson.True, gjson.False:
		return strings.TrimSpace(res.Raw)
	default:
		if res.IsArray() {
			if arr := res.Array(); len(arr) > 0 {
				return strings.TrimSpace(arr[0].String())
			}
			return ""
		}
		return strings.TrimSpace(res.Raw)
	}
}

// evalNextURLCSS 用 goquery 提取下一页：有 @getter 取属性，否则取文本。
func evalNextURLCSS(rule, content string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return "", err
	}
	body, _, _, _ := splitReplace(rule)
	selector, getter := splitGetter(body)
	sel := doc.Selection
	if selector != "" {
		sel = selectElements(sel, selector)
	}
	if sel.Length() == 0 {
		return "", nil
	}
	first := sel.First()
	switch getter {
	case "", "text":
		return strings.TrimSpace(first.Text()), nil
	case "textNodes", "ownText", "html", "all":
		if out := getResultLast(sel, getter); len(out) > 0 {
			return strings.TrimSpace(out[0]), nil
		}
		return "", nil
	default:
		if v, ok := first.Attr(getter); ok {
			return strings.TrimSpace(v), nil
		}
		return "", nil
	}
}
