package parser

import "strings"

// splitGetter 切分字段级选择器 "selector@getter"。@ 必须位于顶层：跳过单引号、
// 双引号与属性选择器 [] 内部的 @，只认最后一个顶层 @。
// 例：`div[data-x="a@b"]@text` → (`div[data-x="a@b"]`, `text`)。
// 本函数与 M1a 的 splitSourceRule 完全独立，互不干扰。
func splitGetter(rule string) (selector, getter string) {
	idx := lastTopLevelAt(rule)
	if idx < 0 {
		return strings.TrimSpace(rule), ""
	}
	return strings.TrimSpace(rule[:idx]), strings.TrimSpace(rule[idx+1:])
}

// splitSelectorChain 按顶层 @ 把选择器链切成多段（列表切分用），
// 引号/中括号内的 @ 不切分。
func splitSelectorChain(rule string) []string {
	var out []string
	start := 0
	var inSingle, inDouble bool
	depth := 0
	for i := 0; i < len(rule); i++ {
		c := rule[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\\' && i+1 < len(rule):
			i++
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case c == '@' && depth == 0:
			if seg := strings.TrimSpace(rule[start:i]); seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	if seg := strings.TrimSpace(rule[start:]); seg != "" {
		out = append(out, seg)
	}
	return out
}

// lastTopLevelAt 返回最后一个位于顶层的 @ 下标（不在引号/中括号内），无则 -1。
func lastTopLevelAt(s string) int {
	var inSingle, inDouble bool
	depth := 0
	last := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\\' && i+1 < len(s):
			i++
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case c == '@' && depth == 0:
			last = i
		}
	}
	return last
}
