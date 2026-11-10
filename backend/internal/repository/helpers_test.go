//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"yreader/backend/internal/repository/testutil"
)

// seedSource 插入一个测试书源。
func seedSource(t *testing.T, q Querier, userID uuid.UUID, tag string) *BookSource {
	t.Helper()
	s := &BookSource{
		UserID:          userID,
		BookSourceURL:   "https://example.com/" + tag,
		BookSourceName:  "源_" + tag,
		BookSourceGroup: "默认",
		Enabled:         true,
		EnabledExplore:  true,
		Weight:          10,
		Header:          json.RawMessage(`{}`),
		Rules:           json.RawMessage(`{"ruleSearch":{"bookList":"//div[@class=\"book\"]"}}`),
	}
	if err := NewSourceRepository().Create(context.Background(), q, s); err != nil {
		t.Fatalf("seed source %s: %v", tag, err)
	}
	return s
}

// seedBook 插入一本测试逻辑书（已归一化入参）。
func seedBook(t *testing.T, q Querier, userID uuid.UUID, sourceID uuid.UUID, tag string) *Book {
	t.Helper()
	b := &Book{
		UserID:           userID,
		BookType:         BookTypeOnline,
		NormalizedName:   "book_" + tag,
		NormalizedAuthor: "author_" + tag,
		Name:             "书籍_" + tag,
		Author:           "作者_" + tag,
		Status:           BookStatusUnknown,
		ActiveSourceID:   &sourceID,
		ActiveBookURL:    ptr("https://example.com/book/" + tag),
		TotalChapters:    0,
	}
	if err := NewBookRepository().Create(context.Background(), q, b); err != nil {
		t.Fatalf("seed book %s: %v", tag, err)
	}
	return b
}

func ptr[T any](v T) *T { return &v }

func mustSeedUser(t *testing.T, tx pgx.Tx) uuid.UUID {
	t.Helper()
	return testutil.SeedUser(t, tx)
}
