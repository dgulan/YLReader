package api

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// ListReplaceRules 返回替换规则；enabled 为可选过滤。
func (s *Server) ListReplaceRules(w http.ResponseWriter, r *http.Request, params ListReplaceRulesParams) {
	uid := userIDFrom(r.Context())
	items, err := s.ReplaceRules.List(r.Context(), uid, params.Enabled)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIReplaceRules(items))
}

// CreateReplaceRule 新建规则；非法正则返回 422 rule_execution_failed。
func (s *Server) CreateReplaceRule(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r.Context())
	var req ReplaceRuleCreate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	in := service.ReplaceRuleInput{
		Name:        req.Name,
		Pattern:     req.Pattern,
		Replacement: strOr(req.Replacement, ""),
		IsEnabled:   true,
	}
	if req.GroupName != nil {
		in.GroupName = *req.GroupName
	}
	if req.IsEnabled != nil {
		in.IsEnabled = *req.IsEnabled
	}
	if req.OrderIndex != nil {
		in.OrderIndex = int32(*req.OrderIndex)
	}
	if req.Scope != nil {
		in.Scope = string(*req.Scope)
	}
	rule, err := s.ReplaceRules.Create(r.Context(), uid, in)
	if err != nil {
		if errors.Is(err, service.ErrInvalidRegex) {
			writeErrorCode(w, r, http.StatusUnprocessableEntity, "rule_execution_failed")
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAPIReplaceRule(rule))
}

// UpdateReplaceRule 更新规则；非法正则返回 400 validation_error。
func (s *Server) UpdateReplaceRule(w http.ResponseWriter, r *http.Request, ruleId BookId) {
	uid := userIDFrom(r.Context())
	var req ReplaceRuleUpdate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	in := service.ReplaceRuleInput{
		Name:        req.Name,
		Pattern:     req.Pattern,
		Replacement: strOr(req.Replacement, ""),
		IsEnabled:   true,
	}
	if req.GroupName != nil {
		in.GroupName = *req.GroupName
	}
	if req.IsEnabled != nil {
		in.IsEnabled = *req.IsEnabled
	}
	if req.OrderIndex != nil {
		in.OrderIndex = int32(*req.OrderIndex)
	}
	if req.Scope != nil {
		in.Scope = string(*req.Scope)
	}
	rule, err := s.ReplaceRules.Update(r.Context(), uid, uuid.UUID(ruleId), in)
	if err != nil {
		if errors.Is(err, service.ErrInvalidRegex) {
			writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			writeErrorCode(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIReplaceRule(rule))
}

// DeleteReplaceRule 删除规则。
func (s *Server) DeleteReplaceRule(w http.ResponseWriter, r *http.Request, ruleId BookId) {
	uid := userIDFrom(r.Context())
	if err := s.ReplaceRules.Delete(r.Context(), uid, uuid.UUID(ruleId)); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeErrorCode(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toAPIReplaceRules(items []*repository.ReplaceRule) []ReplaceRule {
	out := make([]ReplaceRule, 0, len(items))
	for _, item := range items {
		out = append(out, *toAPIReplaceRule(item))
	}
	return out
}

func toAPIReplaceRule(rule *repository.ReplaceRule) *ReplaceRule {
	created := rule.CreatedAt
	updated := rule.UpdatedAt
	group := rule.GroupName
	return &ReplaceRule{
		Id:          rule.ID,
		Name:        rule.Name,
		GroupName:   &group,
		Pattern:     rule.Pattern,
		Replacement: rule.Replacement,
		Scope:       ReplaceRuleScope(rule.Scope),
		IsEnabled:   rule.IsEnabled,
		OrderIndex:  int(rule.OrderIndex),
		CreatedAt:   &created,
		UpdatedAt:   &updated,
	}
}

func strOr(v *string, fallback string) string {
	if v == nil {
		return fallback
	}
	return *v
}
