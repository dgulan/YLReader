package parser

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// fastPath 尝试将 {{...}} 表达式按严格 BNF 求值：
//
//	expr   := term (('+'|'-') term)*
//	term   := factor (('*'|'/'|'%') factor)*
//	factor := INT | IDENT | '(' expr ')'
//
// 出现函数调用、属性访问、三元、字符串拼接、比较、下标、正则字面量时返回
// ok=false，由调用方回落 sandbox。算术语义对齐 JS Number（/ 为浮点除，% 为
// 截断取余，+ 仅数字相加）。
func fastPath(expr string, vars map[string]any) (string, bool) {
	p := &fpParser{src: []rune(strings.TrimSpace(expr)), vars: vars}
	v, ok := p.parseExpr()
	if !ok {
		return "", false
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return "", false
	}
	return formatNumber(v), true
}

type fpParser struct {
	src  []rune
	pos  int
	vars map[string]any
}

func (p *fpParser) skipSpace() {
	for p.pos < len(p.src) && unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
}

func (p *fpParser) parseExpr() (float64, bool) {
	v, ok := p.parseTerm()
	if !ok {
		return 0, false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return v, true
		}
		op := p.src[p.pos]
		if op != '+' && op != '-' {
			return v, true
		}
		p.pos++
		rhs, ok := p.parseTerm()
		if !ok {
			return 0, false
		}
		if op == '+' {
			v += rhs
		} else {
			v -= rhs
		}
	}
}

func (p *fpParser) parseTerm() (float64, bool) {
	v, ok := p.parseFactor()
	if !ok {
		return 0, false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return v, true
		}
		op := p.src[p.pos]
		if op != '*' && op != '/' && op != '%' {
			return v, true
		}
		p.pos++
		rhs, ok := p.parseFactor()
		if !ok {
			return 0, false
		}
		switch op {
		case '*':
			v *= rhs
		case '/':
			v /= rhs
		case '%':
			v = math.Mod(v, rhs)
		}
	}
}

func (p *fpParser) parseFactor() (float64, bool) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return 0, false
	}
	c := p.src[p.pos]
	switch {
	case c == '(':
		p.pos++
		v, ok := p.parseExpr()
		if !ok {
			return 0, false
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return 0, false
		}
		p.pos++
		return v, true
	case c >= '0' && c <= '9':
		return p.parseNumber()
	case isIdentStart(c):
		return p.parseIdent()
	default:
		return 0, false
	}
}

func (p *fpParser) parseNumber() (float64, bool) {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' ||
			((c == '+' || c == '-') && p.pos > start && (p.src[p.pos-1] == 'e' || p.src[p.pos-1] == 'E')) {
			p.pos++
			continue
		}
		break
	}
	v, err := strconv.ParseFloat(string(p.src[start:p.pos]), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (p *fpParser) parseIdent() (float64, bool) {
	start := p.pos
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	name := string(p.src[start:p.pos])
	return resolveNumber(name, p.vars)
}

func isIdentStart(c rune) bool {
	return c == '_' || c == '$' || unicode.IsLetter(c)
}

func isIdentPart(c rune) bool {
	return isIdentStart(c) || unicode.IsDigit(c)
}

// resolveNumber 将标识符解析为数字；非数字值（字符串、bool、nil）返回 false。
func resolveNumber(name string, vars map[string]any) (float64, bool) {
	v, ok := vars[name]
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// formatNumber 将浮点数按 JS Number 语义格式化为字符串（普通小数范围）。
func formatNumber(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	case v == 0:
		return "0" // 覆盖 -0
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
