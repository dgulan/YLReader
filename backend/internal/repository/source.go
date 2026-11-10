package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"yreader/backend/internal/pkg/crypto"
)

// SourceRepository 负责 book_sources 的 CRUD、分页检索与乐观锁更新。
type SourceRepository struct{}

// NewSourceRepository 构造无状态的书源仓库。
func NewSourceRepository() *SourceRepository { return &SourceRepository{} }

const sourceColumns = `id, user_id, book_source_url, book_source_name, book_source_group,
	book_source_type, enabled, enabled_explore, weight, respond_time,
	charset, concurrent_rate, header, login_url, login_check_js, rules,
	failure_count, last_checked_at, last_success_at, version, created_at, updated_at`

func scanSource(row pgx.Row) (*BookSource, error) {
	var s BookSource
	err := row.Scan(
		&s.ID, &s.UserID, &s.BookSourceURL, &s.BookSourceName, &s.BookSourceGroup,
		&s.BookSourceType, &s.Enabled, &s.EnabledExplore, &s.Weight, &s.RespondTime,
		&s.Charset, &s.ConcurrentRate, &s.Header, &s.LoginURL, &s.LoginCheckJS, &s.Rules,
		&s.FailureCount, &s.LastCheckedAt, &s.LastSuccessAt, &s.Version, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// v1.4（ADR-010）：header 落库为 AES-GCM 信封，读取时按 AAD=user_id|source_id 透明解密。
	if err := openSourceHeader(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// sealSourceHeaderValue 返回「写库用」的 header 值：配置了密钥环时封装为
// {"__enc":"v1:<kid>:<b64>"}；未配置或为空则原样（defaultJSON 兜底 '{}'）。
// 注意：不修改 s.Header —— API 响应必须回明文，客户端永远不该看到信封。
func sealSourceHeaderValue(s *BookSource) (json.RawMessage, error) {
	ring := crypto.DefaultKeyring()
	if ring == nil || len(s.Header) == 0 {
		return defaultJSON(s.Header), nil
	}
	var probe struct {
		Enc string `json:"__enc"`
	}
	if err := json.Unmarshal(s.Header, &probe); err == nil && probe.Enc != "" {
		return s.Header, nil // 已是信封（迁移脚本重复执行时幂等）
	}
	if !json.Valid(s.Header) {
		return nil, fmt.Errorf("%w: header is not valid json", ErrInvalidArgument)
	}
	sealed, err := ring.Seal(s.Header, crypto.AAD(s.UserID, s.ID))
	if err != nil {
		return nil, fmt.Errorf("seal header: %w", err)
	}
	wrapped, err := json.Marshal(map[string]string{crypto.EnvelopeKey: sealed})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(wrapped), nil
}

// openSourceHeader 把信封还原为明文；未加密的历史行原样返回（迁移前兼容），
// 但「有 __enc 却解不开」必须报错，严禁静默回退明文。
func openSourceHeader(s *BookSource) error {
	if len(s.Header) == 0 {
		return nil
	}
	var probe struct {
		Enc string `json:"__enc"`
	}
	if err := json.Unmarshal(s.Header, &probe); err != nil || probe.Enc == "" {
		return nil
	}
	ring := crypto.DefaultKeyring()
	if ring == nil {
		return fmt.Errorf("%w: encrypted header but no YREADER_HEADER_KEY_* configured", crypto.ErrNoKey)
	}
	plain, err := ring.Open(probe.Enc, crypto.AAD(s.UserID, s.ID))
	if err != nil {
		return fmt.Errorf("open header: %w", err)
	}
	if !json.Valid(plain) {
		return crypto.ErrMalformed
	}
	s.Header = json.RawMessage(plain)
	return nil
}

func defaultJSON(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage(`{}`)
	}
	return v
}

// Create 插入一条书源并回填 id/version/时间戳。
// id 由调用方（或此处）预生成：AAD 需要 id，因此不允许依赖 DB DEFAULT。
func (r *SourceRepository) Create(ctx context.Context, q Querier, s *BookSource) error {
	if s.BookSourceGroup == "" {
		s.BookSourceGroup = "默认"
	}
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	header, err := sealSourceHeaderValue(s)
	if err != nil {
		return err
	}
	err = q.QueryRow(ctx, `
		INSERT INTO book_sources (
			id, user_id, book_source_url, book_source_name, book_source_group,
			book_source_type, enabled, enabled_explore, weight, respond_time,
			charset, concurrent_rate, header, login_url, login_check_js, rules,
			failure_count, last_checked_at, last_success_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING version, created_at, updated_at`,
		s.ID, s.UserID, s.BookSourceURL, s.BookSourceName, s.BookSourceGroup,
		s.BookSourceType, s.Enabled, s.EnabledExplore, s.Weight, s.RespondTime,
		s.Charset, s.ConcurrentRate, header, s.LoginURL, s.LoginCheckJS, defaultJSON(s.Rules),
		s.FailureCount, s.LastCheckedAt, s.LastSuccessAt,
	).Scan(&s.Version, &s.CreatedAt, &s.UpdatedAt)
	if err != nil && isUniqueViolation(err) {
		// 多 %w：既可用 errors.Is(ErrDuplicate) 判断，也可用 errors.As(*pgconn.PgError) 取约束名。
		return fmt.Errorf("%w: %w", ErrDuplicate, err)
	}
	return err
}

// GetByURL 按用户 + book_source_url 读取书源（本地书库虚拟源自愈用）。
func (r *SourceRepository) GetByURL(ctx context.Context, q Querier, userID uuid.UUID, rawURL string) (*BookSource, error) {
	return scanSource(q.QueryRow(ctx, `SELECT `+sourceColumns+`
		FROM book_sources WHERE user_id = $1 AND book_source_url = $2`, userID, rawURL))
}

// Get 按用户 + 主键读取一条书源。
func (r *SourceRepository) Get(ctx context.Context, q Querier, userID, id uuid.UUID) (*BookSource, error) {
	return scanSource(q.QueryRow(ctx, `SELECT `+sourceColumns+`
		FROM book_sources WHERE user_id = $1 AND id = $2`, userID, id))
}

// ListEnabledForMatch 返回该用户全部启用书源，供章节覆盖按 host 自动匹配
// （v1.1 §4.6.2：多源同 host 取 weight 最高、同权重取 updated_at 最新）。
func (r *SourceRepository) ListEnabledForMatch(ctx context.Context, q Querier, userID uuid.UUID) ([]*BookSource, error) {
	rows, err := q.Query(ctx, `SELECT `+sourceColumns+`
		FROM book_sources WHERE user_id = $1 AND enabled = true
		ORDER BY weight DESC, updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*BookSource, 0, 16)
	for rows.Next() {
		s, serr := scanSource(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SourceListFilter 是书源列表检索的过滤条件。
type SourceListFilter struct {
	Query   string      // 名称模糊或 URL 前缀
	Group   *string     // 分组精确匹配
	Enabled *bool       // 启用状态精确匹配
	IDs     []uuid.UUID // 主键集合（空切片不参与过滤）
	Sort    string      // weight_desc(默认) / name_asc / updated_desc / created_desc
	Limit   int
	Offset  int
	// ExcludeLocal 为 true 时排除本地书库虚拟源（book_source_type = 3）。
	ExcludeLocal bool
}

// List 按用户 + 动态过滤条件分页检索书源，返回当前页记录与总条数。
// 参数化占位符由 $n 动态拼接，排序走固定 switch，杜绝任意字符串注入。
func (r *SourceRepository) List(ctx context.Context, q Querier, userID uuid.UUID, f SourceListFilter) ([]*BookSource, int, error) {
	where := "user_id = $1"
	args := []any{userID}
	n := 2
	if f.Query != "" {
		where += fmt.Sprintf(` AND (book_source_name ILIKE '%%' || $%d || '%%'
			OR (($%d LIKE 'http://%%' OR $%d LIKE 'https://%%') AND book_source_url LIKE $%d || '%%'))`,
			n, n, n, n)
		args = append(args, f.Query)
		n++
	}
	if len(f.IDs) > 0 {
		where += fmt.Sprintf(` AND id = ANY($%d)`, n)
		args = append(args, f.IDs)
		n++
	}
	if f.Group != nil {
		where += fmt.Sprintf(` AND book_source_group = $%d`, n)
		args = append(args, *f.Group)
		n++
	}
	if f.Enabled != nil {
		where += fmt.Sprintf(` AND enabled = $%d`, n)
		args = append(args, *f.Enabled)
		n++
	}
	if f.ExcludeLocal {
		where += ` AND book_source_type <> 3`
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM book_sources WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderBy := "weight DESC, book_source_name, id"
	switch f.Sort {
	case "", "weight_desc":
		orderBy = "weight DESC, book_source_name, id"
	case "name_asc":
		orderBy = "book_source_name, id"
	case "updated_desc":
		orderBy = "updated_at DESC, id"
	case "created_desc":
		orderBy = "created_at DESC, id"
	}

	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := q.Query(ctx, fmt.Sprintf(`SELECT %s FROM book_sources WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
		sourceColumns, where, orderBy, n, n+1), queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]*BookSource, 0, limit)
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Update 以乐观锁更新书源；version 由数据库触发器维护，SQL 中不显式自增。
// 版本不匹配（或记录不存在）时返回 ErrVersionConflict。
func (r *SourceRepository) Update(ctx context.Context, q Querier, s *BookSource, expectedVersion int64) error {
	header, err := sealSourceHeaderValue(s)
	if err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `
		UPDATE book_sources SET
			book_source_url = $3, book_source_name = $4, book_source_group = $5,
			book_source_type = $6, enabled = $7, enabled_explore = $8, weight = $9,
			respond_time = $10, charset = $11, concurrent_rate = $12, header = $13,
			login_url = $14, login_check_js = $15, rules = $16, failure_count = $17,
			last_checked_at = $18, last_success_at = $19
		WHERE user_id = $1 AND id = $2 AND version = $20`,
		s.UserID, s.ID, s.BookSourceURL, s.BookSourceName, s.BookSourceGroup,
		s.BookSourceType, s.Enabled, s.EnabledExplore, s.Weight, s.RespondTime,
		s.Charset, s.ConcurrentRate, header, s.LoginURL, s.LoginCheckJS, defaultJSON(s.Rules),
		s.FailureCount, s.LastCheckedAt, s.LastSuccessAt, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

// UpdateVersioned 是 Update 的便捷变体：更新成功后把触发器自增后的版本回读进 s。
func (r *SourceRepository) UpdateVersioned(ctx context.Context, q Querier, s *BookSource, expectedVersion int64) error {
	header, err := sealSourceHeaderValue(s)
	if err != nil {
		return err
	}
	err = q.QueryRow(ctx, `
		UPDATE book_sources SET
			book_source_url = $3, book_source_name = $4, book_source_group = $5,
			book_source_type = $6, enabled = $7, enabled_explore = $8, weight = $9,
			respond_time = $10, charset = $11, concurrent_rate = $12, header = $13,
			login_url = $14, login_check_js = $15, rules = $16, failure_count = $17,
			last_checked_at = $18, last_success_at = $19
		WHERE user_id = $1 AND id = $2 AND version = $20
		RETURNING version, updated_at`,
		s.UserID, s.ID, s.BookSourceURL, s.BookSourceName, s.BookSourceGroup,
		s.BookSourceType, s.Enabled, s.EnabledExplore, s.Weight, s.RespondTime,
		s.Charset, s.ConcurrentRate, header, s.LoginURL, s.LoginCheckJS, defaultJSON(s.Rules),
		s.FailureCount, s.LastCheckedAt, s.LastSuccessAt, expectedVersion,
	).Scan(&s.Version, &s.UpdatedAt)
	if err == pgx.ErrNoRows {
		return ErrVersionConflict
	}
	return err
}

// Delete 删除一条书源（其 mappings/chapters/overrides 由外键级联清理）。
func (r *SourceRepository) Delete(ctx context.Context, q Querier, userID, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM book_sources WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
