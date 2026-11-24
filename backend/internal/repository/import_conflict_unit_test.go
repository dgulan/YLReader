package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type importConflictQuery struct {
	Querier
	t       *testing.T
	id      uuid.UUID
	user    uuid.UUID
	updates int
}

type scanImportRow func(...any) error

func (row scanImportRow) Scan(dest ...any) error { return row(dest...) }

func (q *importConflictQuery) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if !strings.Contains(sql, "WHERE user_id = $1 AND book_source_url = $2") || args[0] != q.user || args[1] != "https://books.example/" {
		q.t.Fatalf("unexpected source lookup: %s", sql)
	}
	return scanImportRow(func(dest ...any) error {
		*dest[0].(*uuid.UUID) = q.id
		*dest[1].(*string) = "旧名称"
		return nil
	})
}

func (q *importConflictQuery) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.updates++
	if args[0] != q.id || args[1] != q.user || args[2] != "新名称" || args[3] != "新分组" || args[5] != false || args[7] != int32(9) ||
		!strings.Contains(sql, "updated_at = now()") || strings.Contains(sql, "created_at") ||
		!strings.Contains(string(args[14].(json.RawMessage)), `"bookList":"article"`) ||
		!strings.Contains(string(args[14].(json.RawMessage)), `"nextContentUrl":"a.next@href"`) ||
		!strings.Contains(string(args[14].(json.RawMessage)), `"extension":{"enabled":true}`) {
		q.t.Fatal("overwrite did not preserve identity and replace source metadata/rules")
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestImportConflictSkipAndOverwrite(t *testing.T) {
	q := &importConflictQuery{t: t, id: uuid.New(), user: uuid.New()}
	rows := []json.RawMessage{json.RawMessage(`{"bookSourceUrl":"https://books.example/","bookSourceName":"新名称","bookSourceGroup":"新分组","enabled":false,"weight":9,"ruleSearch":{"bookList":"article"},"ruleContent":{"nextContentUrl":"a.next@href"},"extension":{"enabled":true}}`)}
	r := NewImportExportRepository()
	skip, err := r.Import(t.Context(), q, q.user, rows, ImportOptions{})
	if err != nil || skip.Skipped != 1 || len(skip.Duplicates) != 1 || skip.Duplicates[0].Name != "旧名称" || q.updates != 0 {
		t.Fatal(skip, err)
	}
	update, err := r.Import(t.Context(), q, q.user, rows, ImportOptions{Overwrite: "update"})
	if err != nil || update.Updated != 1 || update.Success != 0 || len(update.IDs) != 1 || update.IDs[0] != q.id || q.updates != 1 {
		t.Fatal(update, err)
	}
}
