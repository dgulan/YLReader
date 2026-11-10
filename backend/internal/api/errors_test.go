package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

func TestMapErrorToCode(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"nil", nil, http.StatusOK, ""},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, "source_timeout"},
		{"deadline-wrapped", fmt.Errorf("acquire: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, "source_timeout"},
		// 超时必须优先于 upstream 包装：连接超时被 fetcher 包成 upstream 后仍应是 504。
		{"deadline-before-upstream", fmt.Errorf("%w: %w", fetcher.ErrUpstreamError, context.DeadlineExceeded), http.StatusGatewayTimeout, "source_timeout"},
		{"ssrf", fmt.Errorf("dial: %w", fetcher.ErrSSRFBlocked), http.StatusForbidden, "blocked_private_address"},
		{"unsafe-url", fetcher.ErrUnsafeURL, http.StatusForbidden, "unsafe_url"},
		{"body-too-large", fetcher.ErrBodyTooLarge, http.StatusBadGateway, "response_too_large"},
		{"redirect-limit", fetcher.ErrRedirectTooMany, http.StatusBadGateway, "redirect_limit"},
		{"upstream", fmt.Errorf("%w: boom", fetcher.ErrUpstreamError), http.StatusBadGateway, "source_request_failed"},
		{"sandbox-timeout", sandbox.ErrSandboxTimeout, http.StatusGatewayTimeout, "sandbox_timeout"},
		{"sandbox-output", sandbox.ErrSandboxOutputLimit, http.StatusUnprocessableEntity, "sandbox_output_limit"},
		{"sandbox-panic", sandbox.ErrSandboxPanic, http.StatusInternalServerError, "sandbox_panic"},
		{"unsupported-rule", sandbox.ErrUnsupportedRule, http.StatusUnprocessableEntity, "unsupported_rule"},
		{"rule-execution", sandbox.ErrRuleExecution, http.StatusUnprocessableEntity, "rule_execution_failed"},
		{"rule-syntax", parser.ErrRuleSyntax, http.StatusUnprocessableEntity, "rule_execution_failed"},
		{"not-found", repository.ErrNotFound, http.StatusNotFound, "not_found"},
		{"duplicate", repository.ErrDuplicate, http.StatusConflict, "conflict"},
		{"version-conflict", repository.ErrVersionConflict, http.StatusConflict, "conflict"},
		{"invalid-argument", repository.ErrInvalidArgument, http.StatusBadRequest, "validation_error"},
		// v1.3：通用 422 语义码（DEF-OAS-04/07 闭环）。
		{"unprocessable", fmt.Errorf("%w: local book", service.ErrUnprocessable), http.StatusUnprocessableEntity, "unprocessable_entity"},
		{"internal", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, c := range cases {
		status, code := mapErrorToCode(c.err)
		if status != c.status || code != c.code {
			t.Fatalf("%s: mapErrorToCode = (%d,%q), want (%d,%q)", c.name, status, code, c.status, c.code)
		}
	}
}

func TestMessageForCodeIsSafe(t *testing.T) {
	codes := []string{
		"validation_error", "unprocessable_entity", "unauthorized", "forbidden", "not_found", "conflict",
		"source_request_failed", "source_timeout", "blocked_private_address",
		"sandbox_timeout", "rule_execution_failed", "internal_error",
	}
	for _, code := range codes {
		msg := messageForCode(code)
		if msg == "" {
			t.Fatalf("messageForCode(%q) is empty", code)
		}
		if msg == code {
			t.Fatalf("messageForCode(%q) leaks raw code as message", code)
		}
	}
}
