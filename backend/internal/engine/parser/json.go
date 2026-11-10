package parser

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"yreader/backend/internal/engine/sandbox"
)

var (
	bracketIndex = regexp.MustCompile(`\[(\d+)\]`)
	starIndex    = regexp.MustCompile(`\[\*\]`)
)

// evalJSON 用 gjson 求值 JSONPath 规则（@Json: 或 `$.`/`$[` 前缀），支持
// && / || / %% 组合。数组结果逐项展开。
func evalJSON(rule, content string) ([]string, error) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return []string{content}, nil
	}
	// Jayway 递归下降（`..`）在 gjson 中没有等价语义，静默降级会产出
	// 虚假匹配且无法诊断；此处显式拒绝，由上层统一归为 unsupported_rule。
	if strings.Contains(rule, "..") {
		return nil, fmt.Errorf("%w: recursive descent '..' is not supported", sandbox.ErrUnsupportedRule)
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
		temp := jsonSegment(content, toGJSONPath(seg))
		if len(temp) > 0 {
			results = append(results, temp)
			if op == "||" {
				break
			}
		}
	}
	return combine(results, op), nil
}

// toGJSONPath 把 Legado JSONPath（`$.`/`$[` 前缀、`[0]` 索引、`[*]` 数组展开）
// 转成 gjson 路径。gjson 无 `[*]` 语法，数组由路径本身直接返回。
// 递归下降 `..` 由调用方先行拒绝，这里不做任何静默降级。
func toGJSONPath(rule string) string {
	rule = strings.TrimSpace(rule)
	rule = starIndex.ReplaceAllString(rule, "")
	if strings.HasPrefix(rule, "$.") {
		rule = rule[2:]
	} else if strings.HasPrefix(rule, "$") {
		rule = rule[1:]
	}
	rule = strings.TrimPrefix(rule, ".")
	return bracketIndex.ReplaceAllString(rule, ".$1")
}

// jsonSegment 用 gjson 提取单个 JSONPath，数组展开为多值。
func jsonSegment(content, path string) []string {
	res := gjson.Get(content, path)
	if !res.Exists() {
		return nil
	}
	if res.IsArray() {
		out := make([]string, 0, len(res.Array()))
		for _, item := range res.Array() {
			out = append(out, item.String())
		}
		return out
	}
	return []string{res.String()}
}
