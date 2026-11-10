package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// SettingsRepository 读写 app_settings（每用户每 key 一行，value 为 jsonb）。
type SettingsRepository struct{}

// NewSettingsRepository 构造 SettingsRepository。
func NewSettingsRepository() *SettingsRepository { return &SettingsRepository{} }

// ListAll 返回用户全部设置，key → 原始 JSON 值。
func (r *SettingsRepository) ListAll(ctx context.Context, q Querier, userID uuid.UUID) (map[string]json.RawMessage, error) {
	rows, err := q.Query(ctx,
		`SELECT key, value FROM app_settings WHERE user_id = $1 ORDER BY key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]json.RawMessage)
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		out[key] = json.RawMessage(raw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Set 写入或覆盖单个 key。
func (r *SettingsRepository) Set(ctx context.Context, q Querier, userID uuid.UUID, key string, value json.RawMessage) error {
	_, err := q.Exec(ctx,
		`INSERT INTO app_settings (user_id, key, value, updated_at)
		 VALUES ($1, $2, $3::jsonb, now())
		 ON CONFLICT (user_id, key) DO UPDATE
		 SET value = EXCLUDED.value, updated_at = now()`,
		userID, key, []byte(value))
	return err
}

// Delete 删除单个 key（不存在不报错）。
func (r *SettingsRepository) Delete(ctx context.Context, q Querier, userID uuid.UUID, key string) error {
	_, err := q.Exec(ctx,
		`DELETE FROM app_settings WHERE user_id = $1 AND key = $2`, userID, key)
	return err
}
