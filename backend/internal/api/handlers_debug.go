package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DebugSourceRule 处理 POST /sources/{sourceId}/debug（10s 预算 + 逐级 StepTrace）。
func (s *Server) DebugSourceRule(w http.ResponseWriter, r *http.Request, sourceId SourceId) {
	uid := userIDFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var req DebugRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if !validRuleType(string(req.RuleType)) || strings.TrimSpace(req.Rule) == "" {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}

	maxPreview := 512
	if req.MaxPreviewBytes != nil {
		maxPreview = *req.MaxPreviewBytes
	}
	if maxPreview <= 0 {
		maxPreview = 512
	}
	if maxPreview > 4096 {
		maxPreview = 4096
	}
	var vars map[string]any
	if req.Variables != nil {
		vars = *req.Variables
	}

	start := time.Now()
	result, steps, err := s.Debug.Debug(ctx, uid, sourceId, string(req.RuleType), req.Rule, req.Url, vars, maxPreview)
	if err != nil {
		// 仅前置阶段失败（书源不存在 / 输入不可得 / 抓取失败）走这里。
		writeError(w, r, err)
		return
	}

	out := DebugResult{
		TraceId:        "tr_" + uuid.NewString(),
		Steps:          make([]DebugStep, 0, len(steps)),
		TotalElapsedMs: int(time.Since(start).Milliseconds()),
	}
	if result != "" {
		preview := result
		out.ResultPreview = &preview
	}
	for _, st := range steps {
		phase := st.Phase
		if phase == "" {
			phase = string(req.RuleType)
		}
		step := DebugStep{
			Index:     st.Index,
			Phase:     DebugStepPhase(normalizePhase(phase)),
			Mode:      DebugStepMode(st.Mode),
			Expr:      st.Expr,
			Hit:       st.Hit,
			ElapsedMs: st.ElapsedMs,
		}
		if st.InputDigest != "" {
			v := st.InputDigest
			step.InputDigest = &v
		}
		if st.OutputPreview != "" {
			v := st.OutputPreview
			step.OutputPreview = &v
		}
		if st.OutputItems != 0 {
			v := st.OutputItems
			step.OutputItems = &v
		}
		if st.ErrorCode != "" {
			v := st.ErrorCode
			step.ErrorCode = &v
		}
		out.Steps = append(out.Steps, step)
	}
	writeJSON(w, http.StatusOK, out)
}

func validRuleType(t string) bool {
	switch t {
	case "search", "explore", "book_info", "toc", "content":
		return true
	}
	return false
}

// normalizePhase 强制把驼峰映射回 OpenAPI 的 snake_case 枚举。
func normalizePhase(phase string) string {
	switch phase {
	case "bookInfo", "book_info":
		return "book_info"
	}
	return phase
}
