package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

const defaultSourceFailureTTL = 10 * time.Minute

// SourceHealthStore 抽象书源健康度持久化接口，便于单测注入内存桩。
type SourceHealthStore interface {
	RecordSuccess(ctx context.Context, userID, sourceID uuid.UUID, elapsedMs int32) error
	RecordFailure(ctx context.Context, userID, sourceID uuid.UUID, kind, errorCode, message string, ttl time.Duration) error
}

type pgSourceHealthStore struct {
	pool *pgxpool.Pool
	repo *repository.SourceHealthRepository
}

func (s pgSourceHealthStore) RecordSuccess(ctx context.Context, userID, sourceID uuid.UUID, elapsedMs int32) error {
	return s.repo.RecordSuccess(ctx, s.pool, userID, sourceID, elapsedMs)
}

func (s pgSourceHealthStore) RecordFailure(ctx context.Context, userID, sourceID uuid.UUID, kind, errorCode, message string, ttl time.Duration) error {
	return s.repo.RecordFailure(ctx, s.pool, userID, sourceID, kind, errorCode, message, ttl)
}

// SourceHealthService 负责记录书源成功耗时与脱敏失败归因。
type SourceHealthService struct {
	store SourceHealthStore
	log   *slog.Logger
}

// NewSourceHealthService 构造基于 PostgreSQL 的书源健康度服务。
func NewSourceHealthService(pool *pgxpool.Pool, log *slog.Logger) *SourceHealthService {
	if log == nil {
		log = slog.Default()
	}
	var store SourceHealthStore
	if pool != nil {
		store = pgSourceHealthStore{pool: pool, repo: repository.NewSourceHealthRepository()}
	}
	return &SourceHealthService{store: store, log: log}
}

// NewSourceHealthServiceWithStore 使用自定义存储构造健康度服务（单测用）。
func NewSourceHealthServiceWithStore(store SourceHealthStore, log *slog.Logger) *SourceHealthService {
	if log == nil {
		log = slog.Default()
	}
	return &SourceHealthService{store: store, log: log}
}

// RecordSuccess 在有界上下文内记录书源请求成功耗时。
func (s *SourceHealthService) RecordSuccess(userID, sourceID uuid.UUID, elapsed time.Duration) {
	if s == nil || s.store == nil || userID == uuid.Nil || sourceID == uuid.Nil {
		return
	}
	ms := int32(elapsed.Milliseconds())
	if ms < 0 {
		ms = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := s.store.RecordSuccess(ctx, userID, sourceID, ms); err != nil {
		s.log.Warn("record source health success failed", "source_id", sourceID, "err", err)
	}
}

// RecordFailure 归因并记录书源失败；客户端主动取消（context.Canceled）不计为书源故障。
func (s *SourceHealthService) RecordFailure(userID, sourceID uuid.UUID, err error) {
	if s == nil || s.store == nil || err == nil || userID == uuid.Nil || sourceID == uuid.Nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	kind, code, msg := ClassifySourceFailure(err)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if werr := s.store.RecordFailure(ctx, userID, sourceID, kind, code, msg, defaultSourceFailureTTL); werr != nil {
		s.log.Warn("record source health failure failed", "source_id", sourceID, "err", werr)
	}
}

// ClassifySourceFailure 将抓取/解析错误映射为脱敏的 (failure_kind, error_code, safe_message)。
// 严禁返回 err.Error() 原文，防止目标 URL、请求头或内网 IP 泄漏。
func ClassifySourceFailure(err error) (kind, errorCode, safeMessage string) {
	var statusErr *fetcher.HTTPStatusError
	switch {
	case errors.Is(err, sandbox.ErrSandboxTimeout):
		return repository.FailureKindTimeout, "sandbox_timeout", "sandbox script execution timed out"
	case errors.Is(err, context.DeadlineExceeded):
		return repository.FailureKindTimeout, "source_timeout", "upstream request timed out"
	case errors.Is(err, fetcher.ErrSSRFBlocked):
		return repository.FailureKindBlocked, "blocked_private_address", "request blocked by SSRF policy"
	case errors.Is(err, fetcher.ErrUnsafeURL):
		return repository.FailureKindBlocked, "unsafe_url", "unsafe target URL"
	case errors.Is(err, fetcher.ErrChallengeRequired):
		return repository.FailureKindBlocked, "source_request_failed", "upstream requires browser verification"
	case errors.Is(err, fetcher.ErrRateLimit):
		return repository.FailureKindBlocked, "rate_limited", "domain rate limit exceeded"
	case errors.Is(err, sandbox.ErrUnsupportedRule):
		return repository.FailureKindUnsupported, "unsupported_rule", "unsupported source rule"
	case errors.Is(err, sandbox.ErrSandboxOutputLimit):
		return repository.FailureKindRule, "sandbox_output_limit", "sandbox output limit exceeded"
	case errors.Is(err, sandbox.ErrSandboxPanic):
		return repository.FailureKindRule, "sandbox_panic", "sandbox panic"
	case errors.Is(err, sandbox.ErrRuleExecution), errors.Is(err, parser.ErrRuleSyntax):
		return repository.FailureKindRule, "rule_execution_failed", "rule execution failed"
	case errors.Is(err, fetcher.ErrBodyTooLarge):
		return repository.FailureKindNetwork, "response_too_large", "response body exceeds limit"
	case errors.Is(err, fetcher.ErrRedirectTooMany):
		return repository.FailureKindNetwork, "redirect_limit", "too many redirects"
	case errors.As(err, &statusErr) && (statusErr.StatusCode == 401 || statusErr.StatusCode == 403):
		return repository.FailureKindAuth, "source_request_failed", "upstream authentication or permission error"
	default:
		return repository.FailureKindNetwork, "source_request_failed", "upstream request failed"
	}
}
