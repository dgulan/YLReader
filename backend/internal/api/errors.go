package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// mapErrorToCode 把领域错误映射为 (HTTP 状态码, OpenAPI Error.code 枚举)。
//
// 严禁字符串匹配：全部使用 errors.Is / errors.As，并严格按优先级判定。
// 注意超时必须先于 fetcher.ErrUpstreamError：连接超时会被 fetcher 包装成
// upstream 错误，若不先判 context.DeadlineExceeded 会退化为 502。
func mapErrorToCode(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, ""
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "source_timeout"
	case errors.Is(err, fetcher.ErrSSRFBlocked):
		return http.StatusForbidden, "blocked_private_address"
	case errors.Is(err, fetcher.ErrUnsafeURL):
		return http.StatusForbidden, "unsafe_url"
	case errors.Is(err, fetcher.ErrBodyTooLarge):
		return http.StatusBadGateway, "response_too_large"
	case errors.Is(err, fetcher.ErrRedirectTooMany):
		return http.StatusBadGateway, "redirect_limit"
	case errors.Is(err, fetcher.ErrUpstreamError):
		return http.StatusBadGateway, "source_request_failed"
	case errors.Is(err, sandbox.ErrSandboxTimeout):
		return http.StatusGatewayTimeout, "sandbox_timeout"
	case errors.Is(err, sandbox.ErrSandboxOutputLimit):
		return http.StatusUnprocessableEntity, "sandbox_output_limit"
	case errors.Is(err, sandbox.ErrSandboxPanic):
		return http.StatusInternalServerError, "sandbox_panic"
	case errors.Is(err, sandbox.ErrUnsupportedRule):
		return http.StatusUnprocessableEntity, "unsupported_rule"
	case errors.Is(err, sandbox.ErrRuleExecution), errors.Is(err, parser.ErrRuleSyntax):
		return http.StatusUnprocessableEntity, "rule_execution_failed"
	case errors.Is(err, repository.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, service.ErrJobActive), errors.Is(err, service.ErrJobFinal), errors.Is(err, service.ErrMappingActive):
		return http.StatusConflict, "conflict"
	case errors.Is(err, service.ErrUnprocessable):
		return http.StatusUnprocessableEntity, string(ErrorCodeUnprocessableEntity)
	case errors.Is(err, repository.ErrDuplicate), errors.Is(err, repository.ErrVersionConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, repository.ErrInvalidArgument):
		return http.StatusBadRequest, "validation_error"
	case errors.Is(err, service.ErrNoMatchingSource):
		return http.StatusUnprocessableEntity, "no_matching_source"
	}
	return http.StatusInternalServerError, "internal_error"
}

// messageForCode 返回安全的简短文案，绝不暴露 err.Error()（防止泄漏目标站 URL
// 或内部 IP 轨迹）。/search 的 failures 与顶层错误响应共用该映射。
func messageForCode(code string) string {
	switch code {
	case "validation_error":
		return "invalid request"
	case "unprocessable_entity":
		return "unprocessable entity"
	case "unauthorized":
		return "authentication required"
	case "forbidden":
		return "permission denied"
	case "not_found":
		return "resource not found"
	case "conflict":
		return "resource conflict"
	case "source_disabled":
		return "source disabled"
	case "unsupported_rule":
		return "unsupported rule"
	case "rule_execution_failed":
		return "rule execution failed"
	case "sandbox_output_limit":
		return "sandbox output limit exceeded"
	case "sandbox_timeout":
		return "sandbox timeout"
	case "sandbox_panic":
		return "sandbox panic"
	case "source_request_failed":
		return "source request failed"
	case "source_timeout":
		return "source timeout"
	case "response_too_large":
		return "response too large"
	case "redirect_limit":
		return "too many redirects"
	case "unsafe_url":
		return "unsafe url"
	case "blocked_private_address":
		return "blocked private address"
	case "payload_too_large":
		return "payload too large"
	case "no_matching_source":
		return "no matching source"
	case "override_not_found":
		return "override not found"
	case "rate_limited":
		return "rate limited"
	case "relocation_failed":
		return "relocation failed"
	case "progress_conflict":
		return "progress conflict"
	case "precondition_required":
		return "precondition required"
	}
	return "internal error"
}

// writeError 用统一映射写出错误响应。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := mapErrorToCode(err)
	writeErrorCode(w, r, status, code)
}

// writeErrorCode 以指定状态码与枚举码写出 ErrorResponse。
func writeErrorCode(w http.ResponseWriter, r *http.Request, status int, code string) {
	resp := ErrorResponse{Error: Error{
		Code:    ErrorCode(code),
		Message: messageForCode(code),
	}}
	if id := requestIDFrom(r.Context()); id != "" {
		resp.Error.RequestId = &id
	}
	writeJSON(w, status, resp)
}

// writeJSON 写出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
