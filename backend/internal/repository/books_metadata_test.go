//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"yreader/backend/internal/pkg/textnorm"
	"yreader/backend/internal/repository/testutil"
)

func TestBookRenameUpdatesNormalizedIdentity(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	user := mustSeedUser(t, tx)
	source := seedSource(t, tx, user, "metadata")
	book := seedBook(t, tx, user, source.ID, "before")
	other := seedBook(t, tx, user, source.ID, "other")
	repo := NewBookRepository()
	ctx := context.Background()
	name, author := "新书名", "新作者"
	updated, err := repo.UpdateMeta(ctx, tx, user, book.ID, BookPatch{Name: &name, Author: &author}, book.Version)
	if err != nil {
		t.Fatal(err)
	}
	found, err := repo.FindByNormalized(ctx, tx, user, textnorm.NormalizeBook(name), textnorm.NormalizeAuthor(author))
	if err != nil || found.ID != book.ID {
		t.Fatalf("new identity: %+v %v", found, err)
	}
	if _, err := repo.FindByNormalized(ctx, tx, user, book.NormalizedName, book.NormalizedAuthor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale identity: %v", err)
	}
	if _, err := repo.UpdateMeta(ctx, tx, user, book.ID, BookPatch{Name: &name}, book.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version: %v (new %d)", err, updated.Version)
	}
	// Last operation: PostgreSQL marks this transaction aborted on uniqueness failure.
	if _, err := repo.UpdateMeta(ctx, tx, user, other.ID, BookPatch{Name: &name, Author: &author}, other.Version); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate identity: %v", err)
	}
}
