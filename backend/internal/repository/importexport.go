package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ImportExportRepository 负责 Legado 3.0 书源的导入与导出。
//
// 核心约束：不得用强类型结构体逐层反序列化。导入时以 json.RawMessage
// 保留每一条书源的完整原始 JSON，整段落库到 book_sources.rules，
// 因此顶层与嵌套的未知键都会原样透传；导出时以 rules 为基底，
// 再把数据库关系列按 Legado camelCase 字段名回写。
type ImportExportRepository struct{}

// NewImportExportRepository 构造导入导出仓库。
func NewImportExportRepository() *ImportExportRepository { return &ImportExportRepository{} }

// ImportOptions 控制导入行为。
type ImportOptions struct {
	// Overwrite 决定同 URL 已存在时的处理方式："skip"（默认）或 "update"。
	Overwrite string
	// DryRun 仅供 Service 层做「演练后回滚」；Repository 始终完整落库，
	// 以保证返回的 ImportReport 绝对准确。
	DryRun bool
}

// ImportFailure 记录单条导入失败（不中断整批）。
type ImportFailure struct {
	Index   int    `json:"index"`
	URL     string `json:"url,omitempty"`
	Message string `json:"message"`
}

// ImportReport 是导入的统计报告。
type ImportReport struct {
	Total    int             `json:"total"`
	Success  int             `json:"success"`
	Failed   int             `json:"failed"`
	Skipped  int             `json:"skipped"`
	Updated  int             `json:"updated"`
	IDs      []uuid.UUID     `json:"ids"`
	Failures []ImportFailure `json:"failures,omitempty"`
}

// Import 逐条导入书源。单条非法只计入 Failed 并继续；同 URL 存在时按
// opts.Overwrite（skip 默认 / update）分别统计。返回统计报告。
func (r *ImportExportRepository) Import(ctx context.Context, q Querier, userID uuid.UUID, sources []json.RawMessage, opts ImportOptions) (*ImportReport, error) {
	overwrite := opts.Overwrite
	if overwrite != "update" {
		overwrite = "skip"
	}
	rep := &ImportReport{Total: len(sources), IDs: make([]uuid.UUID, 0, len(sources))}
	srcRepo := NewSourceRepository()

	for i, raw := range sources {
		s, err := parseSource(raw, userID)
		if err != nil {
			rep.Failed++
			rep.Failures = append(rep.Failures, ImportFailure{Index: i, Message: err.Error()})
			continue
		}

		var existingID uuid.UUID
		err = q.QueryRow(ctx, `SELECT id FROM book_sources WHERE user_id = $1 AND book_source_url = $2`,
			userID, s.BookSourceURL).Scan(&existingID)
		switch {
		case err == nil:
			if overwrite == "update" {
				if uerr := r.updateSource(ctx, q, existingID, userID, s); uerr != nil {
					rep.Failed++
					rep.Failures = append(rep.Failures, ImportFailure{Index: i, URL: s.BookSourceURL, Message: uerr.Error()})
					continue
				}
				rep.Updated++
				rep.IDs = append(rep.IDs, existingID)
			} else {
				rep.Skipped++
			}
		case err == pgx.ErrNoRows:
			if cerr := srcRepo.Create(ctx, q, s); cerr != nil {
				rep.Failed++
				rep.Failures = append(rep.Failures, ImportFailure{Index: i, URL: s.BookSourceURL, Message: cerr.Error()})
				continue
			}
			rep.Success++
			rep.IDs = append(rep.IDs, s.ID)
		default:
			rep.Failed++
			rep.Failures = append(rep.Failures, ImportFailure{Index: i, URL: s.BookSourceURL, Message: err.Error()})
		}
	}
	return rep, nil
}

// parseSource 把单条原始 JSON 解析为 BookSource（关系列 + 原始 rules）。
func parseSource(raw json.RawMessage, userID uuid.UUID) (*BookSource, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	url := rawString(obj["bookSourceUrl"])
	if url == nil || *url == "" {
		return nil, fmt.Errorf("%w: missing bookSourceUrl", ErrInvalidArgument)
	}
	// 存储契约（schema ck_book_sources_url_scheme）要求 http(s) URL；
	// 非 URL 的 Legado 自定义 bookSourceUrl 直接判为单条非法。
	if !strings.HasPrefix(*url, "http://") && !strings.HasPrefix(*url, "https://") {
		return nil, fmt.Errorf("%w: bookSourceUrl %q is not http(s)", ErrInvalidArgument, *url)
	}
	name := rawString(obj["bookSourceName"])
	if name == nil || *name == "" {
		name = url
	}

	// 存储契约收敛：真实 Legado 样本存在超出 schema CHECK 的值
	// （如 bookSourceType=3、respondTime<0），入库时按契约钳制，
	// 但不修改 rules 里的原始值（未知键透传）。
	bsType := rawInt16(obj["bookSourceType"], 0)
	if bsType < 0 || bsType > 2 {
		bsType = 0
	}
	respondTime := rawInt32(obj["respondTime"], 0)
	if respondTime < 0 {
		respondTime = 0
	}

	return &BookSource{
		UserID:          userID,
		BookSourceURL:   *url,
		BookSourceName:  *name,
		BookSourceGroup: stringOr(obj["bookSourceGroup"], "默认"),
		BookSourceType:  bsType,
		Enabled:         rawBool(obj["enabled"], true),
		EnabledExplore:  rawBool(obj["enabledExplore"], true),
		Weight:          rawInt32(obj["weight"], 0),
		RespondTime:     respondTime,
		Charset:         rawString(obj["charset"]),
		ConcurrentRate:  rawString(obj["concurrentRate"]),
		LoginURL:        rawString(obj["loginUrl"]),
		LoginCheckJS:    rawString(obj["loginCheckJs"]),
		Header:          extractHeader(obj),
		Rules:           raw, // 整段原始 JSON，未知键透传
	}, nil
}

// updateSource 按 id + user 覆盖书源关系列与 rules（供 overwrite=update）。
func (r *ImportExportRepository) updateSource(ctx context.Context, q Querier, id, userID uuid.UUID, s *BookSource) error {
	// DEF-SEC-01 / S5-1：overwrite=update 走独立 UPDATE（不经 SourceRepository.Create），
	// 必须在此显式密封 header，否则批量导入的 Cookie/Token 会明文落库。
	if s.ID == uuid.Nil {
		s.ID = id
	}
	header, hErr := sealSourceHeaderValue(s)
	if hErr != nil {
		return hErr
	}
	tag, err := q.Exec(ctx, `UPDATE book_sources SET
			book_source_name = $3, book_source_group = $4, book_source_type = $5,
			enabled = $6, enabled_explore = $7, weight = $8, respond_time = $9,
			charset = $10, concurrent_rate = $11, header = $12, login_url = $13,
			login_check_js = $14, rules = $15
		WHERE id = $1 AND user_id = $2`,
		id, userID, s.BookSourceName, s.BookSourceGroup, s.BookSourceType,
		s.Enabled, s.EnabledExplore, s.Weight, s.RespondTime,
		s.Charset, s.ConcurrentRate, header, s.LoginURL, s.LoginCheckJS, defaultJSON(s.Rules))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Export 导出书源：以 rules 原始 JSON 为基底，把数据库关系列按 Legado 3.0
// camelCase 字段名回写（关系列权威），未知键原样保留。ids 为空导出全部。
func (r *ImportExportRepository) Export(ctx context.Context, q Querier, userID uuid.UUID, ids []uuid.UUID) ([]json.RawMessage, error) {
	where := "user_id = $1"
	args := []any{userID}
	if len(ids) > 0 {
		where += " AND id = ANY($2)"
		args = append(args, ids)
	}
	rows, err := q.Query(ctx, `SELECT rules, book_source_url, book_source_name, book_source_group,
			book_source_type, enabled, enabled_explore, weight, respond_time,
			charset, concurrent_rate, header, login_url, login_check_js
		FROM book_sources WHERE `+where+`
		ORDER BY book_source_name, book_source_url, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]json.RawMessage, 0, 64)
	for rows.Next() {
		var raw json.RawMessage
		var s BookSource
		if err := rows.Scan(&raw, &s.BookSourceURL, &s.BookSourceName, &s.BookSourceGroup,
			&s.BookSourceType, &s.Enabled, &s.EnabledExplore, &s.Weight, &s.RespondTime,
			&s.Charset, &s.ConcurrentRate, &s.Header, &s.LoginURL, &s.LoginCheckJS); err != nil {
			return nil, err
		}
		out = append(out, mergeSource(raw, &s))
	}
	return out, rows.Err()
}

// mergeSource 把关系列回写到原始 JSON 对象（Legado camelCase 键名）。
func mergeSource(raw json.RawMessage, s *BookSource) json.RawMessage {
	obj := map[string]json.RawMessage{}
	if len(raw) > 0 {
		var existing map[string]json.RawMessage
		if json.Unmarshal(raw, &existing) == nil && existing != nil {
			obj = existing
		}
	}
	set := func(k string, v any) {
		if b, err := json.Marshal(v); err == nil {
			obj[k] = b
		}
	}
	set("bookSourceUrl", s.BookSourceURL)
	set("bookSourceName", s.BookSourceName)
	set("bookSourceGroup", s.BookSourceGroup)
	set("bookSourceType", s.BookSourceType)
	set("enabled", s.Enabled)
	set("enabledExplore", s.EnabledExplore)
	set("weight", s.Weight)
	set("respondTime", s.RespondTime)
	set("charset", deref(s.Charset))
	set("concurrentRate", deref(s.ConcurrentRate))
	set("loginUrl", deref(s.LoginURL))
	set("loginCheckJs", deref(s.LoginCheckJS))
	if len(s.Header) > 0 && json.Valid(s.Header) {
		obj["header"] = s.Header
	} else {
		obj["header"] = json.RawMessage(`{}`)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return b
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func rawString(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return &s
	}
	return nil
}

func stringOr(raw json.RawMessage, def string) string {
	if s := rawString(raw); s != nil && *s != "" {
		return *s
	}
	return def
}

func rawBool(raw json.RawMessage, def bool) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return def
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	return def
}

func rawInt32(raw json.RawMessage, def int32) int32 {
	if len(raw) == 0 || string(raw) == "null" {
		return def
	}
	var n int32
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int32(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		var parsed int32
		if _, err := fmt.Sscanf(s, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return def
}

func rawInt16(raw json.RawMessage, def int16) int16 {
	return int16(rawInt32(raw, int32(def)))
}

// extractHeader 把 Legado 的 header（通常是「字符串里套 JSON」）解析为对象；
// 解析失败回退空对象，绝不阻塞导入。
func extractHeader(obj map[string]json.RawMessage) json.RawMessage {
	raw, ok := obj["header"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		return json.RawMessage(`{}`)
	}
	if json.Valid(raw) {
		return raw
	}
	return json.RawMessage(`{}`)
}
