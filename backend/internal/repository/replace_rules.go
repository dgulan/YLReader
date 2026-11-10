package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ReplaceRule 是用户级正文替换净化规则（schema.sql: replace_rules）。
// 注意：表中没有 is_regex 列，pattern 一律按 Go regexp（RE2）编译；
// 也没有 book_id 列，作用域由 scope 表达。
type ReplaceRule struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Name        string
	GroupName   string
	Pattern     string
	Replacement string
	Scope       string
	IsEnabled   bool
	OrderIndex  int32
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReplaceRuleRepository 提供替换规则的读写能力。
type ReplaceRuleRepository struct{}

// NewReplaceRuleRepository 构造 ReplaceRuleRepository。
func NewReplaceRuleRepository() *ReplaceRuleRepository { return &ReplaceRuleRepository{} }

const replaceRuleColumns = `id, user_id, name, group_name, pattern, replacement, scope, is_enabled, order_index, created_at, updated_at`

// ListEnabledForContent 读取当前用户启用且作用于正文（content/all）的规则，
// 按 order_index 升序，供正文净化链顺序应用。
func (r *ReplaceRuleRepository) ListEnabledForContent(ctx context.Context, q Querier, userID uuid.UUID) ([]*ReplaceRule, error) {
	rows, err := q.Query(ctx, `
		SELECT id, name, group_name, pattern, replacement, scope, is_enabled, order_index
		FROM replace_rules
		WHERE user_id = $1 AND scope IN ('content', 'all') AND is_enabled = true
		ORDER BY order_index ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*ReplaceRule, 0, 16)
	for rows.Next() {
		var rule ReplaceRule
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.GroupName, &rule.Pattern, &rule.Replacement,
			&rule.Scope, &rule.IsEnabled, &rule.OrderIndex); err != nil {
			return nil, err
		}
		out = append(out, &rule)
	}
	return out, rows.Err()
}

func scanReplaceRule(row interface {
	Scan(dest ...any) error
}) (*ReplaceRule, error) {
	var r ReplaceRule
	if err := row.Scan(&r.ID, &r.UserID, &r.Name, &r.GroupName, &r.Pattern, &r.Replacement,
		&r.Scope, &r.IsEnabled, &r.OrderIndex, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// Create 插入规则并回填服务端字段。
func (r *ReplaceRuleRepository) Create(ctx context.Context, q Querier, rule *ReplaceRule) error {
	row := q.QueryRow(ctx,
		`INSERT INTO replace_rules (user_id, name, group_name, pattern, replacement, scope, is_enabled, order_index)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, created_at, updated_at`,
		rule.UserID, rule.Name, rule.GroupName, rule.Pattern, rule.Replacement,
		rule.Scope, rule.IsEnabled, rule.OrderIndex)
	return row.Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)
}

// List 返回用户规则；enabled 为 nil 表示不过滤。
func (r *ReplaceRuleRepository) List(ctx context.Context, q Querier, userID uuid.UUID, enabled *bool) ([]*ReplaceRule, error) {
	rows, err := q.Query(ctx,
		`SELECT `+replaceRuleColumns+`
		   FROM replace_rules
		  WHERE user_id = $1 AND ($2::boolean IS NULL OR is_enabled = $2)
		  ORDER BY order_index ASC, created_at DESC`,
		userID, enabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*ReplaceRule, 0)
	for rows.Next() {
		item, err := scanReplaceRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Get 按 id 取规则（限本用户）。
func (r *ReplaceRuleRepository) Get(ctx context.Context, q Querier, userID, ruleID uuid.UUID) (*ReplaceRule, error) {
	row := q.QueryRow(ctx,
		`SELECT `+replaceRuleColumns+` FROM replace_rules WHERE user_id = $1 AND id = $2`,
		userID, ruleID)
	item, err := scanReplaceRule(row)
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return item, nil
}

// Update 整体更新规则（限本用户）；未命中返回 ErrNotFound。
func (r *ReplaceRuleRepository) Update(ctx context.Context, q Querier, rule *ReplaceRule) error {
	row := q.QueryRow(ctx,
		`UPDATE replace_rules
		    SET name = $3, group_name = $4, pattern = $5, replacement = $6,
		        scope = $7, is_enabled = $8, order_index = $9
		  WHERE user_id = $1 AND id = $2
		  RETURNING created_at, updated_at`,
		rule.UserID, rule.ID, rule.Name, rule.GroupName, rule.Pattern, rule.Replacement,
		rule.Scope, rule.IsEnabled, rule.OrderIndex)
	if err := row.Scan(&rule.CreatedAt, &rule.UpdatedAt); err != nil {
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// Delete 删除规则（限本用户）；未命中返回 ErrNotFound。
func (r *ReplaceRuleRepository) Delete(ctx context.Context, q Querier, userID, ruleID uuid.UUID) error {
	tag, err := q.Exec(ctx,
		`DELETE FROM replace_rules WHERE user_id = $1 AND id = $2`, userID, ruleID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
