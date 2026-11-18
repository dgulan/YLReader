package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"yreader/backend/internal/repository"
)

func tocStateRows(urls ...string) []repository.Chapter {
	rows := make([]repository.Chapter, len(urls))
	for i, url := range urls {
		index := int32(i)
		target := "https://example.com/" + url
		rows[i] = repository.Chapter{ChapterIndex: &index, SortOrder: index, URL: target, URLHash: fmt.Sprintf("%x", sha256.Sum256([]byte(target))), Title: url, NormalizedTitle: url}
	}
	return rows
}
func tocStatePointers(rows []repository.Chapter) []*repository.Chapter {
	out := make([]*repository.Chapter, len(rows))
	for i := range rows {
		out[i] = &rows[i]
	}
	return out
}
func TestTOCStateExactURLProtection(t *testing.T) {
	old := tocStatePointers(tocStateRows("a", "b", "c"))
	for _, tc := range []struct {
		name  string
		next  []repository.Chapter
		index int32
		want  int32
		fail  bool
	}{
		{"swap", tocStateRows("b", "a", "c"), 0, 1, false},
		{"missing", tocStateRows("b", "c"), 0, 0, true},
		{"ambiguous", tocStateRows("b", "a", "a"), 0, 0, true},
		{"same position wins", tocStateRows("a", "a", "c"), 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := planTOCPositions(old, tc.next)
			if err != nil {
				t.Fatal(err)
			}
			ch, err := p.target(tc.index)
			if tc.fail {
				if !errors.Is(err, ErrRelocationFailed) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || *ch.ChapterIndex != tc.want {
				t.Fatal(ch, err)
			}
		})
	}
	p, _ := planTOCPositions(old, tocStateRows("a", "b", "c", "d"))
	if p.changed {
		t.Fatal("append changed existing positions")
	}
}
func TestTOCStateOverrideAndBookmarkCollision(t *testing.T) {
	p, _ := planTOCPositions(tocStatePointers(tocStateRows("a", "b")), tocStateRows("b", "a"))
	if _, _, err := p.relocate(repository.TOCReferences{Overrides: []*repository.ChapterSourceOverride{{ChapterIndex: 0}}}); !errors.Is(err, ErrRelocationFailed) {
		t.Fatal(err)
	}
	p, _ = planTOCPositions(tocStatePointers(tocStateRows("a", "a", "c")), tocStateRows("c", "b", "a"))
	if _, _, err := p.relocate(repository.TOCReferences{Bookmarks: []*repository.Bookmark{{ChapterIndex: 0, CharOffset: 5}, {ChapterIndex: 1, CharOffset: 5}}}); !errors.Is(err, ErrRelocationFailed) {
		t.Fatal(err)
	}
}
