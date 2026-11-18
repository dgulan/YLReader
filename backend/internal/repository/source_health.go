package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// FailureKind 定义 source_failures.kind 枚举值（与 docs/schema.sql 对齐）。
const (
	FailureKindNetwork     = "network"
	FailureKindTimeout     = "timeout"
	FailureKindRule        = "rule"
	FailureKindBlocked     = "blocked"
	FailureKindUnsupported = "unsupported"
	FailureKindAuth        = "auth"
)

// SourceHealthRepository 负责更新 book_sources 健康度指标与写入 source_failures 归因记录。
type SourceHealthRepository struct{}

// NewSourceHealthRepository 构造书源健康度仓储。
func NewSourceHealthRepository() *SourceHealthRepository { return &SourceHealthRepository{} }

// RecordSuccess 清零 failure_count、更新 respond_time（EWMA）与最后成功时间，并关闭活跃失败记录。
func (r *SourceHealthRepository) RecordSuccess(ctx context.Context, q Querier, userID, sourceID uuid.UUID, elapsedMs int32) error {
	if elapsedMs < 0 {
		elapsedMs = 0
	}
	if _, err := q.Exec(ctx, `
		UPDATE book_sources
		   SET failure_count = 0,
		       respond_time = CASE
		         WHEN respond_time = 0 THEN $3
		         ELSE ((respond_time * 3 + $3) / 4)::integer
		       END,
		       last_checked_at = now(),
		       last_success_at = now()
		 WHERE user_id = $1 AND id = $2`,
		userID, sourceID, elapsedMs,
	); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `
		UPDATE source_failures
		   SET resolved_at = now()
		 WHERE user_id = $1 AND source_id = $2 AND resolved_at IS NULL`,
		userID, sourceID,
	)
	return err
}

// RecordFailure 递增 failure_count、更新 last_checked_at，并写入脱敏后的 source_failures 归因记录。
func (r *SourceHealthRepository) RecordFailure(ctx context.Context, q Querier, userID, sourceID uuid.UUID, kind, errorCode, message string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	kind = normalizeFailureKind(kind)
	if errorCode == "" {
		errorCode = "source_request_failed"
	}
	if len(message) > 512 {
		message = message[:512]
	}

	if _, err := q.Exec(ctx, `
		UPDATE book_sources
		   SET failure_count = failure_count + 1,
		       last_checked_at = now()
		 WHERE user_id = $1 AND id = $2`,
		userID, sourceID,
	); err != nil {
		return err
	}

	occurred := time.Now().UTC()
	expires := occurred.Add(ttl)
	_, err := q.Exec(ctx, `
		INSERT INTO source_failures (user_id, source_id, kind, error_code, message, occurred_at, expires_at)
		VALUES ($1, $2, $3::failure_kind, $4, $5, $6, $7)`,
		userID, sourceID, kind, errorCode, message, occurred, expires,
	)
	return err
}

func normalizeFailureKind(kind string) string {
	switch kind {
	case FailureKindNetwork, FailureKindTimeout, FailureKindRule, FailureKindBlocked, FailureKindUnsupported, FailureKindAuth:
		return kind
	default:
		return FailureKindNetwork
	}
}
