package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// ErrInvalidSettingsKey 表示 settings 的 key 不符合契约约束。
var ErrInvalidSettingsKey = errors.New("invalid settings key")

// settingsKeyRe 与 schema.sql 的 CHECK 约束保持一致。
var settingsKeyRe = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)

// SettingsService 读写用户设置（app_settings 逐 key 一行）。
type SettingsService struct {
	pool *pgxpool.Pool
	repo *repository.SettingsRepository
	log  *slog.Logger
}

// NewSettingsService 构造 SettingsService。
func NewSettingsService(pool *pgxpool.Pool, log *slog.Logger) *SettingsService {
	if log == nil {
		log = slog.Default()
	}
	return &SettingsService{pool: pool, repo: repository.NewSettingsRepository(), log: log}
}

// Get 返回全部设置；无记录时返回空 map（不返回 nil）。
func (s *SettingsService) Get(ctx context.Context, userID uuid.UUID) (map[string]json.RawMessage, error) {
	values, err := s.repo.ListAll(ctx, s.pool, userID)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	return values, nil
}

// Patch 浅合并：值为 null 表示删除，其余为写入/覆盖；返回更新后的全量设置。
func (s *SettingsService) Patch(ctx context.Context, userID uuid.UUID, patch map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	keys := make([]string, 0, len(patch))
	for key, raw := range patch {
		if !settingsKeyRe.MatchString(key) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidSettingsKey, key)
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("%w: %q has invalid json value", ErrInvalidSettingsKey, key)
		}
		keys = append(keys, key)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	for _, key := range keys {
		raw := patch[key]
		if string(raw) == "null" {
			if err := s.repo.Delete(ctx, tx, userID, key); err != nil {
				return nil, fmt.Errorf("delete setting %q: %w", key, err)
			}
			continue
		}
		if err := s.repo.Set(ctx, tx, userID, key, raw); err != nil {
			return nil, fmt.Errorf("set setting %q: %w", key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID)
}
