package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Job 是 jobs 表的一行（后台异步任务）。
type Job struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Type         string
	Status       string
	Payload      json.RawMessage
	Result       *json.RawMessage
	Progress     int16
	ErrorCode    *string
	ErrorMessage *string
	Attempts     int16
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	ExpiresAt    *time.Time
	WorkerID     *string
}

const jobColumns = `id, user_id, type, status, payload, result, progress, error_code, error_message,
	attempts, created_at, started_at, finished_at, expires_at, worker_id`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.UserID, &j.Type, &j.Status, &j.Payload, &j.Result, &j.Progress,
		&j.ErrorCode, &j.ErrorMessage, &j.Attempts, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.ExpiresAt, &j.WorkerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &j, nil
}

// JobRepository 访问 jobs 表。
type JobRepository struct{}

// NewJobRepository 构造任务仓储。
func NewJobRepository() *JobRepository { return &JobRepository{} }

// Create 插入一条 queued 任务。
func (r *JobRepository) Create(ctx context.Context, q Querier, userID uuid.UUID, jobType string, payload json.RawMessage) (*Job, error) {
	return scanJob(q.QueryRow(ctx, `INSERT INTO jobs (user_id, type, payload)
		VALUES ($1, $2::job_type, $3)
		RETURNING `+jobColumns, userID, jobType, payload))
}

// GetByID 读取当前用户的任务。
func (r *JobRepository) GetByID(ctx context.Context, q Querier, userID, jobID uuid.UUID) (*Job, error) {
	return scanJob(q.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1 AND user_id = $2`, jobID, userID))
}

// GetAny 按 id 读取任务（不限定用户；仅供 Worker 执行时使用）。
func (r *JobRepository) GetAny(ctx context.Context, q Querier, jobID uuid.UUID) (*Job, error) {
	return scanJob(q.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1`, jobID))
}

// GetStatus 只读任务状态（ExecuteJob 的取消感知用）。
func (r *JobRepository) GetStatus(ctx context.Context, q Querier, jobID uuid.UUID) (string, error) {
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return status, nil
}

// HasActiveFullCache 判断该书 + 目标源是否已有进行中的 full_cache 任务。
// S5-2 起防重粒度为 (book, source)：同书不同源可并行预热。
func (r *JobRepository) HasActiveFullCache(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs
		WHERE user_id = $1 AND type = 'full_cache' AND status IN ('queued','running')
		  AND payload->>'book_id' = $2 AND payload->>'source_id' = $3)`,
		userID, bookID.String(), sourceID.String()).Scan(&exists)
	return exists, err
}

// HasMappingForSource 判断 (book, source) 是否已建立书源映射（含 pending/stale）。
func (r *JobRepository) HasMappingForSource(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM book_source_mappings m
		JOIN books b ON b.id = m.book_id AND b.user_id = $1
		WHERE m.book_id = $2 AND m.source_id = $3)`, userID, bookID, sourceID).Scan(&exists)
	return exists, err
}

// UpdateProgress 更新进度（0-100）。
func (r *JobRepository) UpdateProgress(ctx context.Context, q Querier, jobID uuid.UUID, progress int16) error {
	_, err := q.Exec(ctx, `UPDATE jobs SET progress = $2 WHERE id = $1 AND status = 'running'`, jobID, progress)
	return err
}

// MarkSucceeded 标记成功并写入结果。
func (r *JobRepository) MarkSucceeded(ctx context.Context, q Querier, jobID uuid.UUID, progress int16, result map[string]any) error {
	var raw []byte
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		raw = b
	}
	_, err := q.Exec(ctx, `UPDATE jobs SET status = 'succeeded', progress = $2, result = $3,
		finished_at = now() WHERE id = $1 AND status IN ('queued','running')`, jobID, progress, raw)
	return err
}

// MarkFailed 标记失败。
func (r *JobRepository) MarkFailed(ctx context.Context, q Querier, jobID uuid.UUID, code, message string) error {
	_, err := q.Exec(ctx, `UPDATE jobs SET status = 'failed', error_code = $2, error_message = $3,
		finished_at = now() WHERE id = $1 AND status IN ('queued','running')`, jobID, code, message)
	return err
}

// Cancel 把 queued/running 置为 canceled；返回是否命中。
func (r *JobRepository) Cancel(ctx context.Context, q Querier, userID, jobID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE jobs SET status = 'canceled', finished_at = now()
		WHERE id = $1 AND user_id = $2 AND status IN ('queued','running')`, jobID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkStaleRunningAsFailed 回收遗留 running 任务（v1.3 Worker 租约自愈）。
//
// 三条准则（OR 关系）：
//  1. 同主机旧实例遗留：worker_id 以 hostPrefix 开头且不是当前活跃进程 → 秒级自愈；
//  2. 跨主机宕机残留：started_at 早于 2 小时租约上限 → 兜底回收；
//  3. 升级前 worker_id 为 NULL 的历史残留 → 立即回收。
//
// 跨主机且未超时的活跃任务受保护，绝不被其他节点误杀。
func (r *JobRepository) MarkStaleRunningAsFailed(ctx context.Context, q Querier, code, message, hostPrefix, currentWorkerID string) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE jobs SET status = 'failed', error_code = $1, error_message = $2,
		finished_at = now()
		WHERE status = 'running'
		  AND (
		    (worker_id LIKE $3 AND worker_id <> $4)
		    OR (started_at < now() - interval '2 hours')
		    OR (worker_id IS NULL)
		  )`, code, message, hostPrefix+"%", currentWorkerID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// IsUniqueViolation 供服务层识别 23505（同 job 防重等场景）。
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
