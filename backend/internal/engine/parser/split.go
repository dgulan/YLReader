// Package parser 实现 Legado 书源规则引擎：规则切分、模式识别（CSS/XPath/JSON/
// Regex/JS）、{{ }} Fast-Path、组合规则（&& / || / %%）、@put/@get 变量、
// $n 回填与 ## 正则替换。语义对齐 references/changshengyu_reader-dev 的
// io.legado.app.model.analyzeRule 包，仅对拍输出结果，不复制代码。
package parser

import (
	"errors"
	"strings"
)

// ErrRuleSyntax 表示规则括号/引号不平衡或语法非法。
var ErrRuleSyntax = errors.New("parser: unbalanced rule brackets")

// splitRule 在顶层按给定分隔符切分规则。顶层指不在 [] / () / {} 括号组内、
// 不在引号内。返回切分出的段落与命中的分隔符（未命中为空串）。括号不平衡
// 返回 ErrRuleSyntax。实现对齐 Legado RuleAnalyzer.splitRule 的语义：只按
// 第一个命中的顶层分隔符切分整条规则，JSONPath 内 [?(...&&...)] 的 && 不会被
// 误切（因为位于 [] 内）。
func splitRule(rule string, splits ...string) ([]string, string, error) {
	op, idx, err := firstTopLevel(rule, splits...)
	if err != nil {
		return nil, "", err
	}
	if op == "" {
		return []string{rule}, "", nil
	}
	return splitBy(rule, op, idx), op, nil
}

// firstTopLevel 找到第一个位于顶层的分隔符。
func firstTopLevel(rule string, splits ...string) (string, int, error) {
	var stack []byte // 开括号栈
	inSingle, inDouble := false, false
	for i := 0; i < len(rule); i++ {
		c := rule[i]
		if c == '\\' { // 引号外的转义字符转义下一个字符
			i++
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if inSingle || inDouble {
			continue
		}
		switch c {
		case '[', '(', '{':
			stack = append(stack, c)
		case ']':
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return "", 0, ErrRuleSyntax
			}
			stack = stack[:len(stack)-1]
		case ')':
			if len(stack) == 0 || stack[len(stack)-1] != '(' {
				return "", 0, ErrRuleSyntax
			}
			stack = stack[:len(stack)-1]
		case '}':
			if len(stack) == 0 || stack[len(stack)-1] != '{' {
				return "", 0, ErrRuleSyntax
			}
			stack = stack[:len(stack)-1]
		default:
			if len(stack) == 0 {
				for _, s := range splits {
					if i+len(s) <= len(rule) && rule[i:i+len(s)] == s {
						return s, i, nil
					}
				}
			}
		}
	}
	if len(stack) != 0 {
		return "", 0, ErrRuleSyntax
	}
	return "", 0, nil
}

// splitBy 按给定分隔符在顶层位置切分整条规则。startIdx 为首个分隔符位置，
// 用于加速；实际仍完整扫描以正确处理括号组。
func splitBy(rule, op string, startIdx int) []string {
	var out []string
	last := 0
	var stack []byte
	inSingle, inDouble := false, false
	for i := 0; i < len(rule); i++ {
		c := rule[i]
		if c == '\\' {
			i++
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if inSingle || inDouble {
			continue
		}
		switch c {
		case '[', '(', '{':
			stack = append(stack, c)
		case ']', ')', '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			if len(stack) == 0 && i+len(op) <= len(rule) && rule[i:i+len(op)] == op {
				out = append(out, rule[last:i])
				last = i + len(op)
				i += len(op) - 1
			}
		}
	}
	out = append(out, rule[last:])
	return out
}

// splitSourceRule 按 JS 块切分规则字符串。JS 块段落（<js>...</js> 或
// @js:... 直到下一个 @js: 或末尾）的 mode 为 ModeJS，其余为 ModeDefault
// （后续由 detectMode 再识别具体模式）。
func splitSourceRule(rule string) []sourceRule {
	var out []sourceRule
	i, n := 0, len(rule)
	for i < n {
		jsIdx, jsKind := -1, 0 // 0=<js>...</js>，1=@js:...
		if idx := strings.Index(rule[i:], "<js>"); idx >= 0 {
			jsIdx = i + idx
		}
		if idx := strings.Index(rule[i:], "@js:"); idx >= 0 {
			if jsIdx < 0 || i+idx < jsIdx {
				jsIdx = i + idx
				jsKind = 1
			}
		}
		if jsIdx < 0 {
			if seg := trimSpace(rule[i:]); seg != "" {
				out = append(out, sourceRule{raw: seg})
			}
			break
		}
		if seg := trimSpace(rule[i:jsIdx]); seg != "" {
			out = append(out, sourceRule{raw: seg})
		}
		if jsKind == 0 {
			// <js>...</js>
			end := strings.Index(rule[jsIdx+4:], "</js>")
			if end < 0 {
				out = append(out, sourceRule{raw: trimSpace(rule[jsIdx+4:]), mode: ModeJS})
				break
			}
			end += jsIdx + 4
			out = append(out, sourceRule{raw: trimSpace(rule[jsIdx+4 : end]), mode: ModeJS})
			i = end + len("</js>")
		} else {
			// @js:... 直到下一个 @js: 或末尾
			next := strings.Index(rule[jsIdx+4:], "@js:")
			if next < 0 {
				out = append(out, sourceRule{raw: trimSpace(rule[jsIdx+4:]), mode: ModeJS})
				break
			}
			out = append(out, sourceRule{raw: trimSpace(rule[jsIdx+4 : jsIdx+4+next]), mode: ModeJS})
			i = jsIdx + 4 + next
		}
	}
	return out
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for i < j && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
