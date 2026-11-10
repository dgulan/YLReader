//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"yreader/backend/internal/repository/testutil"
)

func TestSourceCRUD(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	repo := NewSourceRepository()
	ctx := context.Background()

	// Create
	s := &BookSource{
		UserID:          userID,
		BookSourceURL:   "https://src.example/one",
		BookSourceName:  "示例源一",
		BookSourceGroup: "默认",
		Enabled:         true,
		EnabledExplore:  true,
		Weight:          5,
		Header:          json.RawMessage(`{}`),
		Rules:           json.RawMessage(`{"ruleSearch":{"bookList":"//a"},"unknownTop":123}`),
	}
	if err := repo.Create(ctx, tx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID == [16]byte{} {
		t.Fatal("Create 未回填 id")
	}
	if s.Version != 1 {
		t.Fatalf("Create version = %d, want 1", s.Version)
	}

	// Get
	got, err := repo.Get(ctx, tx, userID, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BookSourceName != "示例源一" || got.Weight != 5 {
		t.Fatalf("Get 字段不匹配: %+v", got)
	}
	if string(got.Rules) == "" {
		t.Fatal("rules 为空")
	}

	// Update with optimistic lock
	got.BookSourceName = "示例源一改"
	if err := repo.UpdateVersioned(ctx, tx, got, s.Version); err != nil {
		t.Fatalf("UpdateVersioned: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("Update 后 version = %d, want 2", got.Version)
	}

	// Stale version
	got.Weight = 99
	if err := repo.Update(ctx, tx, got, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale Update err = %v, want ErrVersionConflict", err)
	}

	// Delete
	if err := repo.Delete(ctx, tx, userID, s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, tx, userID, s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
}

func TestSourceDuplicateURL(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	repo := NewSourceRepository()
	ctx := context.Background()

	s1 := seedSource(t, tx, userID, "dup")
	s2 := &BookSource{
		UserID: s1.UserID, BookSourceURL: s1.BookSourceURL, BookSourceName: "dup2",
		Header: json.RawMessage(`{}`), Rules: json.RawMessage(`{}`),
	}
	if err := repo.Create(ctx, tx, s2); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate Create err = %v, want ErrDuplicate", err)
	}
}

func TestSourceListQ(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	repo := NewSourceRepository()
	ctx := context.Background()

	seedSource(t, tx, userID, "alpha")
	seedSource(t, tx, userID, "beta")
	seedSource(t, tx, userID, "gamma")

	// q 为空 → 全部
	all, total, err := repo.List(ctx, tx, userID, SourceListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("List all total=%d len=%d, want 3/3", total, len(all))
	}

	// 名称模糊
	byName, total, err := repo.List(ctx, tx, userID, SourceListFilter{Query: "beta", Limit: 10})
	if err != nil {
		t.Fatalf("List name: %v", err)
	}
	if total != 1 || len(byName) != 1 || byName[0].BookSourceName != "源_beta" {
		t.Fatalf("List name 结果不符: total=%d %+v", total, byName)
	}

	// URL 前缀
	byURL, total, err := repo.List(ctx, tx, userID, SourceListFilter{Query: "https://example.com/gam", Limit: 10})
	if err != nil {
		t.Fatalf("List url: %v", err)
	}
	if total != 1 || len(byURL) != 1 || byURL[0].BookSourceURL != "https://example.com/gamma" {
		t.Fatalf("List url 结果不符: total=%d %+v", total, byURL)
	}

	// 分页
	page2, total, err := repo.List(ctx, tx, userID, SourceListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if total != 3 || len(page2) != 1 {
		t.Fatalf("分页结果 total=%d len=%d, want 3/1", total, len(page2))
	}
}

// TestSourceListFilter 覆盖动态过滤（group/enabled/ids）与排序兜底、limit 钳制。
func TestSourceListFilter(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	repo := NewSourceRepository()
	ctx := context.Background()

	mk := func(url, name, group string, weight int32, enabled bool) *BookSource {
		t.Helper()
		s := &BookSource{
			UserID:          userID,
			BookSourceURL:   url,
			BookSourceName:  name,
			BookSourceGroup: group,
			Enabled:         enabled,
			EnabledExplore:  true,
			Weight:          weight,
			Header:          json.RawMessage(`{}`),
			Rules:           json.RawMessage(`{}`),
		}
		if err := repo.Create(ctx, tx, s); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return s
	}
	s1 := mk("https://f.example/1", "a_low", "g1", 5, true)
	s2 := mk("https://f.example/2", "b_high", "g1", 50, true)
	s3 := mk("https://f.example/3", "c_mid", "g2", 20, false)

	// 分组过滤
	got, total, err := repo.List(ctx, tx, userID, SourceListFilter{Group: ptr("g1"), Limit: 10})
	if err != nil {
		t.Fatalf("group filter: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("group filter total=%d len=%d, want 2/2", total, len(got))
	}

	// enabled 过滤
	got, total, err = repo.List(ctx, tx, userID, SourceListFilter{Enabled: ptr(true), Limit: 10})
	if err != nil {
		t.Fatalf("enabled filter: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("enabled filter total=%d len=%d, want 2/2", total, len(got))
	}

	// IDs 过滤（空切片必须不拼条件，非空才过滤）
	got, total, err = repo.List(ctx, tx, userID, SourceListFilter{IDs: []uuid.UUID{s2.ID}, Limit: 10})
	if err != nil {
		t.Fatalf("ids filter: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].ID != s2.ID {
		t.Fatalf("ids filter 结果不符: total=%d %+v", total, got)
	}
	if _, total, err = repo.List(ctx, tx, userID, SourceListFilter{IDs: []uuid.UUID{}, Limit: 10}); err != nil || total != 3 {
		t.Fatalf("空 IDs 不得拼条件: total=%d err=%v", total, err)
	}

	// 排序：默认 weight_desc
	got, _, err = repo.List(ctx, tx, userID, SourceListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("sort default: %v", err)
	}
	if len(got) != 3 || got[0].ID != s2.ID || got[1].ID != s3.ID || got[2].ID != s1.ID {
		t.Fatalf("weight_desc 顺序不符: %+v", []uuid.UUID{got[0].ID, got[1].ID, got[2].ID})
	}
	// 排序：name_asc
	got, _, err = repo.List(ctx, tx, userID, SourceListFilter{Sort: "name_asc", Limit: 10})
	if err != nil {
		t.Fatalf("sort name_asc: %v", err)
	}
	if len(got) != 3 || got[0].ID != s1.ID || got[1].ID != s2.ID || got[2].ID != s3.ID {
		t.Fatalf("name_asc 顺序不符: %+v", []uuid.UUID{got[0].ID, got[1].ID, got[2].ID})
	}
	// 未知排序枚举 → 兜底 weight_desc
	got, _, err = repo.List(ctx, tx, userID, SourceListFilter{Sort: "bogus", Limit: 10})
	if err != nil {
		t.Fatalf("sort bogus: %v", err)
	}
	if len(got) != 3 || got[0].ID != s2.ID {
		t.Fatalf("bogus 排序未兜底 weight_desc: %+v", got)
	}
	// limit 钳制：>100 截断为 100（此处仅 3 行，验证无报错且返回全部）
	got, total, err = repo.List(ctx, tx, userID, SourceListFilter{Limit: 1000})
	if err != nil || total != 3 || len(got) != 3 {
		t.Fatalf("limit clamp: total=%d len=%d err=%v", total, len(got), err)
	}
}
