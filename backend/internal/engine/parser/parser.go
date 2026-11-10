package parser

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"yreader/backend/internal/engine/sandbox"
)

// Mode 是规则的模式（求值方式）。
type Mode int

const (
	// ModeDefault 表示 CSS 选择器（默认）。
	ModeDefault Mode = iota
	// ModeXPath 表示 XPath。
	ModeXPath
	// ModeJSON 表示 JSONPath。
	ModeJSON
	// ModeRegex 表示正则。
	ModeRegex
	// ModeJS 表示 JS 规则。
	ModeJS
)

// Engine 是规则求值引擎。vars 保存 @put/@get 变量，跨 Eval 调用共享。
// 变量值保持 any 原样（JSON 反序列化得到的 float64 不截断为 int）。
type Engine struct {
	sb   *sandbox.Sandbox
	vars map[string]any
}

// New 构建规则引擎。sb 为 nil 时 JS 规则与 {{ }} 回落会返回 ErrRuleExecution。
func New(sb *sandbox.Sandbox) *Engine {
	return &Engine{sb: sb, vars: map[string]any{}}
}

// SetVariableAny 设置任意类型的 @put/@get 变量，保持值原样（不做类型转换）。
func (e *Engine) SetVariableAny(k string, v any) {
	e.vars[k] = v
}

// SetVariable 是 SetVariableAny 的字符串便捷入口。
func (e *Engine) SetVariable(k, v string) {
	e.SetVariableAny(k, v)
}

// GetVariable 读取变量并导出为字符串（用于 @get:{key} 的规则字符串回填）；
// 未命中返回空串。
func (e *Engine) GetVariable(k string) string {
	v, ok := e.vars[k]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// sourceRule 是一条解析后的规则段落。
type sourceRule struct {
	raw          string // 原始段落（调试用）
	mode         Mode
	rule         string // 去除 @put 后的规则体（仍含 ##、{{}}、@get、$n）
	put          map[string]string
	literal      bool   // $n 回填触发的 Regex：结果已是字面量，不再做正则匹配
	traceMode    string // StepTrace.Mode 字符串（对齐 DebugStep.mode 枚举）
	fromTemplate bool   // literal 由 {{...}} 触发（traceMode 标为 template）
}

var (
	putPattern    = regexp.MustCompile(`(?i)@put:\{([^}]+?)\}`)
	getPattern    = regexp.MustCompile(`(?i)@get:\{([^}]+?)\}`)
	tmplPattern   = regexp.MustCompile(`\{\{([\s\S]*?)\}\}`)
	dollarPattern = regexp.MustCompile(`\$\d{1,2}`)
)

// Eval 求值规则，返回字符串结果（列表用 "\n" 连接，对齐 Legado getString）。
func (e *Engine) Eval(ctx context.Context, rule, source string) (string, error) {
	list, err := e.EvalList(ctx, rule, source)
	if err != nil {
		return "", err
	}
	return html.UnescapeString(strings.Join(list, "\n")), nil
}

// EvalWithTrace 同 Eval，但额外返回逐级 StepTrace。phase 为调试阶段
// （search/explore/book_info/toc/content）；maxPreviewBytes 为 OutputPreview
// 上限（0 取默认 512，最大 4096）。
func (e *Engine) EvalWithTrace(ctx context.Context, phase, rule, source string, maxPreviewBytes int) (string, []StepTrace, error) {
	list, steps, err := e.EvalListWithTrace(ctx, phase, rule, source, maxPreviewBytes)
	if err != nil {
		return "", steps, err
	}
	return html.UnescapeString(strings.Join(list, "\n")), steps, nil
}

// EvalList 求值规则，返回列表结果（对齐 Legado getStringList）。
func (e *Engine) EvalList(ctx context.Context, rule, source string) ([]string, error) {
	return e.evalList(ctx, rule, source, nil)
}

// EvalListWithTrace 同 EvalList，但额外返回逐级 StepTrace。
func (e *Engine) EvalListWithTrace(ctx context.Context, phase, rule, source string, maxPreviewBytes int) ([]string, []StepTrace, error) {
	rec := newTraceRecorder(phase, maxPreviewBytes)
	list, err := e.evalList(ctx, rule, source, rec)
	return list, rec.steps, err
}

// evalList 是 EvalList / EvalListWithTrace 的公共实现。rec 为 nil 时不追踪。
func (e *Engine) evalList(ctx context.Context, rule, source string, rec *traceRecorder) ([]string, error) {
	rules := splitSourceRule(rule)
	if len(rules) == 0 {
		return []string{}, nil
	}
	for i := range rules {
		if err := e.initRule(&rules[i]); err != nil {
			return nil, err
		}
	}

	result := []string{source}
	for i := range rules {
		sr := &rules[i]
		for k, v := range sr.put {
			val, err := e.Eval(ctx, v, source)
			if err != nil {
				return nil, err
			}
			e.SetVariable(k, val)
		}
		made, err := e.makeUp(ctx, sr.rule, result)
		if err != nil {
			return nil, err
		}
		body, repl, replacement, first := splitReplace(made)

		input := ""
		if len(result) > 0 {
			input = result[0]
		}

		start := time.Now()
		var next []string
		var evalErr error
		if body != "" {
			if sr.mode == ModeRegex && sr.literal {
				next = []string{body}
			} else {
				next, evalErr = e.evalMode(ctx, sr.mode, body, result)
			}
		} else {
			next = result
		}
		if evalErr == nil && repl != "" {
			next = applyReplace(next, repl, replacement, first)
		}
		rec.record(sr.traceMode, sr.rule, input, start, next, evalErr)
		if evalErr != nil {
			return nil, evalErr
		}
		result = next
	}
	return result, nil
}

// initRule 解析模式、@put 与 `$n` 引发的 Regex 模式。
func (e *Engine) initRule(sr *sourceRule) error {
	explicitCSS := false
	if sr.mode == ModeJS {
		// splitSourceRule 已把 <js>...</js> / @js:... 段落标记为 ModeJS，
		// 这里保留，避免 detectMode 把它们误判为 CSS。
		sr.rule = sr.raw
	} else {
		explicitCSS = hasCSSPrefix(sr.raw)
		sr.mode, sr.rule = detectMode(sr.raw)
	}
	sr.put = map[string]string{}
	for _, m := range putPattern.FindAllStringSubmatch(sr.rule, -1) {
		sr.rule = strings.Replace(sr.rule, m[0], "", 1)
		for _, kv := range strings.Split(m[1], ",") {
			kv = strings.TrimSpace(kv)
			if i := strings.Index(kv, ":"); i > 0 {
				sr.put[strings.Trim(strings.TrimSpace(kv[:i]), `"`)] = strings.Trim(strings.TrimSpace(kv[i+1:]), `"`)
			}
		}
	}
	sr.rule = strings.TrimSpace(sr.rule)
	if sr.mode != ModeJS && sr.mode != ModeRegex {
		// {{...}} / @get:{...} 命中且无 ## → 字面量（对齐 Legado evalPattern：
		// start==0 或 !contains("##") 时 mode=Regex 直接返回）。
		if (tmplPattern.MatchString(sr.rule) || getPattern.MatchString(sr.rule)) && !strings.Contains(sr.rule, "##") {
			sr.mode = ModeRegex
			sr.literal = true
			if tmplPattern.MatchString(sr.rule) {
				sr.fromTemplate = true
			}
		} else if dollarPattern.MatchString(sr.rule) {
			// $n 回填 → 字面量（结果已回填，不再做正则匹配）。
			sr.mode = ModeRegex
			sr.literal = true
		}
	}
	sr.traceMode = traceModeString(sr.mode, explicitCSS, sr.fromTemplate)
	return nil
}

// hasCSSPrefix 判断规则是否为显式 CSS 前缀（@css: / @@）。
func hasCSSPrefix(rule string) bool {
	return strings.HasPrefix(rule, "@css:") || strings.HasPrefix(rule, "@CSS:") ||
		strings.HasPrefix(rule, "@@")
}

// traceModeString 把内部 Mode 映射为 DebugStep.mode 枚举。
func traceModeString(m Mode, explicitCSS, fromTemplate bool) string {
	switch m {
	case ModeJS:
		return "js"
	case ModeXPath:
		return "xpath"
	case ModeJSON:
		return "json"
	case ModeRegex:
		if fromTemplate {
			return "template"
		}
		return "regex"
	default:
		if explicitCSS {
			return "css"
		}
		return "default"
	}
}

// detectMode 识别规则模式并剥离模式前缀。
func detectMode(rule string) (Mode, string) {
	switch {
	case strings.HasPrefix(rule, "@css:") || strings.HasPrefix(rule, "@CSS:"):
		return ModeDefault, rule[5:]
	case strings.HasPrefix(rule, "@@"):
		return ModeDefault, rule[2:]
	case strings.HasPrefix(rule, "@XPath:") || strings.HasPrefix(rule, "@xpath:"):
		return ModeXPath, rule[7:]
	case strings.HasPrefix(rule, "@Json:") || strings.HasPrefix(rule, "@json:"):
		return ModeJSON, rule[6:]
	case strings.HasPrefix(rule, "$.") || strings.HasPrefix(rule, "$["):
		return ModeJSON, rule
	case strings.HasPrefix(rule, "/"):
		return ModeXPath, rule
	case strings.HasPrefix(rule, ":"):
		return ModeRegex, rule[1:]
	default:
		return ModeDefault, rule
	}
}

// makeUp 替换 `$n` 回填、{{...}} 模板与 @get:{key} 变量。
func (e *Engine) makeUp(ctx context.Context, rule string, result []string) (string, error) {
	out := dollarPattern.ReplaceAllStringFunc(rule, func(m string) string {
		var n int
		for _, c := range m[1:] {
			n = n*10 + int(c-'0')
		}
		if n < len(result) {
			return result[n]
		}
		return m
	})

	for {
		loc := getPattern.FindStringIndex(out)
		if loc == nil {
			break
		}
		key := getPattern.FindStringSubmatch(out)[1]
		out = out[:loc[0]] + e.GetVariable(key) + out[loc[1]:]
	}

	for {
		loc := tmplPattern.FindStringIndex(out)
		if loc == nil {
			break
		}
		sub := tmplPattern.FindStringSubmatch(out)
		expr := sub[1]
		content := ""
		if len(result) > 0 {
			content = result[0]
		}
		val, err := e.evalTemplate(ctx, expr, content)
		if err != nil {
			return "", err
		}
		out = out[:loc[0]] + val + out[loc[1]:]
	}
	return strings.TrimSpace(out), nil
}

// evalTemplate 求值 {{...}} 内容：优先 Fast-Path，否则回落 sandbox。
func (e *Engine) evalTemplate(ctx context.Context, expr, content string) (string, error) {
	expr = strings.TrimSpace(expr)
	if isRule(expr) {
		return e.Eval(ctx, expr, content)
	}
	vars := map[string]any{"result": content}
	for k, v := range e.vars {
		vars[k] = v
	}
	if s, ok := fastPath(expr, vars); ok {
		return s, nil
	}
	if e.sb == nil {
		return "", sandbox.ErrRuleExecution
	}
	return e.sb.Eval(ctx, expr, vars)
}

// evalMode 按模式求值规则体，返回列表结果。
func (e *Engine) evalMode(ctx context.Context, mode Mode, rule string, result []string) ([]string, error) {
	content := ""
	if len(result) > 0 {
		content = result[0]
	}
	switch mode {
	case ModeJS:
		vars := map[string]any{"result": content}
		for k, v := range e.vars {
			vars[k] = v
		}
		if e.sb == nil {
			return nil, sandbox.ErrRuleExecution
		}
		out, err := e.sb.Eval(ctx, rule, vars)
		if err != nil {
			return nil, err
		}
		return []string{out}, nil
	case ModeXPath:
		return evalXPath(rule, content)
	case ModeJSON:
		return evalJSON(rule, content)
	case ModeRegex:
		return evalRegex(rule, content), nil
	default:
		return evalCSS(rule, content)
	}
}

// isRule 判断 {{...}} 内表达式是否为嵌套规则而非 JS。
func isRule(expr string) bool {
	return strings.HasPrefix(expr, "@") ||
		strings.HasPrefix(expr, "$.") ||
		strings.HasPrefix(expr, "$[") ||
		strings.HasPrefix(expr, "//")
}

// splitReplace 按 ## 切分规则体：body##replaceRegex##replacement##first。
func splitReplace(rule string) (body, repl, replacement string, first bool) {
	parts := strings.Split(rule, "##")
	body = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		repl = parts[1]
	}
	if len(parts) > 2 {
		replacement = parts[2]
	}
	if len(parts) > 3 {
		first = true
	}
	return
}

// applyReplace 对列表逐项做正则替换。re 为 RE2 语法（与 Legado Java 正则存在
// 差异，仅支持 RE2 子集）。
func applyReplace(list []string, re, replacement string, first bool) []string {
	rege, err := regexp.Compile(re)
	if err != nil {
		return list
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		if first {
			loc := rege.FindStringIndex(s)
			if loc == nil {
				out = append(out, s)
				continue
			}
			out = append(out, s[:loc[0]]+rege.ReplaceAllString(s[loc[0]:loc[1]], replacement)+s[loc[1]:])
		} else {
			out = append(out, rege.ReplaceAllString(s, replacement))
		}
	}
	return out
}
