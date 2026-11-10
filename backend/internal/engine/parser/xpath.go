package parser

import (
	"strings"

	"github.com/antchfx/htmlquery"
)

// evalXPath 用 htmlquery 求值 XPath 规则（@XPath: 或 `/` 前缀），支持
// && / || / %% 组合。InnerText 同时覆盖元素文本、text() 文本节点与
// @attr 属性节点（htmlquery 会把属性节点包装成带文本子节点的元素）。
func evalXPath(rule, content string) ([]string, error) {
	doc, err := htmlquery.Parse(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return []string{content}, nil
	}
	segs, op, err := splitRule(rule, "&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	var results [][]string
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		nodes := htmlquery.Find(doc, seg)
		temp := make([]string, 0, len(nodes))
		for _, n := range nodes {
			if s := htmlquery.InnerText(n); s != "" {
				temp = append(temp, s)
			}
		}
		if len(temp) > 0 {
			results = append(results, temp)
			if op == "||" {
				break
			}
		}
	}
	return combine(results, op), nil
}
