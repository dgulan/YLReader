package parser

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"yreader/backend/internal/engine/sandbox"
)

// StepTrace 记录单条规则求值的观测信息。字段严格对齐 docs/openapi.yaml 的
// DebugStep schema（唯一结构来源）。architecture.md §8.1 的 step/expr_sha8/
// input_bytes 只是其简化日志投影，不是本结构的来源。
type StepTrace struct {
	Index         int    `json:"index"`
	Phase         string `json:"phase"`
	Mode          string `json:"mode"`
	Expr          string `json:"expr"`
	InputDigest   string `json:"input_digest"`
	Hit           bool   `json:"hit"`
	OutputPreview string `json:"output_preview"`
	OutputItems   int    `json:"output_items"`
	ElapsedMs     int    `json:"elapsed_ms"`
	ErrorCode     string `json:"error_code,omitempty"`
}

const (
	// DefaultPreviewBytes 是 OutputPreview 的默认上限（DebugStep 默认 512）。
	DefaultPreviewBytes = 512
	// MaxPreviewBytes 是 OutputPreview 的最大上限（DebugStep 最大 4096）。
	MaxPreviewBytes = 4096
)

// traceRecorder 累积 StepTrace。nil 表示不追踪（Eval/EvalList 路径）。
type traceRecorder struct {
	phase      string
	maxPreview int
	steps      []StepTrace
	nextIndex  int
}

func newTraceRecorder(phase string, maxPreviewBytes int) *traceRecorder {
	if maxPreviewBytes <= 0 {
		maxPreviewBytes = DefaultPreviewBytes
	}
	if maxPreviewBytes > MaxPreviewBytes {
		maxPreviewBytes = MaxPreviewBytes
	}
	return &traceRecorder{phase: phase, maxPreview: maxPreviewBytes, nextIndex: 1}
}

func (r *traceRecorder) record(mode, expr, input string, start time.Time, out []string, err error) {
	if r == nil {
		return
	}
	r.steps = append(r.steps, StepTrace{
		Index:         r.nextIndex,
		Phase:         r.phase,
		Mode:          mode,
		Expr:          expr,
		InputDigest:   inputDigest(input),
		Hit:           len(out) > 0,
		OutputPreview: outputPreview(out, r.maxPreview),
		OutputItems:   len(out),
		ElapsedMs:     int(time.Since(start).Milliseconds()),
		ErrorCode:     errorCode(err),
	})
	r.nextIndex++
}

// inputDigest 返回输入摘要：sha256 前 8 位十六进制 + ":" + 字节长度。
func inputDigest(input string) string {
	sum := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x:%d", sum[:4], len(input))
}

// outputPreview 连接输出列表并截断到 maxBytes（不切断 UTF-8 字符）。
func outputPreview(out []string, maxBytes int) string {
	s := strings.Join(out, "\n")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// errorCode 把错误映射为 openapi.yaml Error.code 的枚举值。
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, sandbox.ErrSandboxTimeout):
		return "sandbox_timeout"
	case errors.Is(err, sandbox.ErrSandboxOutputLimit):
		return "sandbox_output_limit"
	case errors.Is(err, sandbox.ErrSandboxPanic):
		return "sandbox_panic"
	case errors.Is(err, sandbox.ErrUnsupportedRule):
		return "unsupported_rule"
	case errors.Is(err, sandbox.ErrRuleExecution):
		return "rule_execution_failed"
	case errors.Is(err, ErrRuleSyntax):
		return "rule_execution_failed"
	default:
		return "rule_execution_failed"
	}
}
