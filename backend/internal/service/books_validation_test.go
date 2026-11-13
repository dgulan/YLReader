package service

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"yreader/backend/internal/repository"
)

func TestBookMetadataRejectsInvalidInputBeforeDatabase(t *testing.T) {
	blank := " \t "
	invalid := repository.BookStatus("invalid")
	for _, patch := range []repository.BookPatch{{Name: &blank}, {Status: &invalid}} {
		_, err := NewBookService(nil, nil, nil).Update(context.Background(), uuid.New(), uuid.New(), patch, 1)
		if !errors.Is(err, repository.ErrInvalidArgument) {
			t.Fatalf("invalid metadata: %v", err)
		}
	}
}
