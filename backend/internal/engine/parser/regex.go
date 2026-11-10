package parser

import "regexp"

// evalRegex 用 RE2 求值正则规则（`:` 前缀触发）。提取每个匹配的 group 1
// （无分组则取整个匹配）。
func evalRegex(rule, content string) []string {
	if rule == "" {
		return []string{content}
	}
	re, err := regexp.Compile(rule)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range re.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			out = append(out, m[1])
		} else {
			out = append(out, m[0])
		}
	}
	return out
}
