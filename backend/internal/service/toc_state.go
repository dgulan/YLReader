package service

import (
	"fmt"
	"math"

	"yreader/backend/internal/repository"
)

type tocPositions struct {
	old     map[int32]string
	next    map[int32]repository.Chapter
	urls    map[string][]int32
	changed bool
	total   int
}

func planTOCPositions(old []*repository.Chapter, next []repository.Chapter) (tocPositions, error) {
	p := tocPositions{old: map[int32]string{}, next: map[int32]repository.Chapter{}, urls: map[string][]int32{}}
	for _, ch := range old {
		if !ch.IsVolume && ch.ChapterIndex != nil {
			p.old[*ch.ChapterIndex] = ch.URL
		}
	}
	for _, ch := range next {
		if ch.IsVolume || ch.ChapterIndex == nil {
			continue
		}
		i := *ch.ChapterIndex
		if _, ok := p.next[i]; ok {
			return p, ErrRelocationFailed
		}
		p.next[i] = ch
		p.urls[ch.URL] = append(p.urls[ch.URL], i)
		p.total++
	}
	for i, url := range p.old {
		if ch, ok := p.next[i]; !ok || ch.URL != url {
			p.changed = true
		}
	}
	return p, nil
}
func (p tocPositions) target(index int32) (repository.Chapter, error) {
	url, ok := p.old[index]
	if !ok || url == "" {
		return repository.Chapter{}, ErrRelocationFailed
	}
	if ch, ok := p.next[index]; ok && ch.URL == url {
		return ch, nil
	}
	if hits := p.urls[url]; len(hits) == 1 {
		return p.next[hits[0]], nil
	}
	return repository.Chapter{}, ErrRelocationFailed
}
func (p tocPositions) relocate(refs repository.TOCReferences) (*repository.ReadingProgress, []repository.BookmarkTOCMove, error) {
	for _, o := range refs.Overrides {
		if o.ChapterIndex >= int32(p.total) {
			continue
		} // Existing out-of-range stale policy.
		ch, err := p.target(o.ChapterIndex)
		if err != nil || *ch.ChapterIndex != o.ChapterIndex {
			return nil, nil, ErrRelocationFailed
		}
	}
	moves := make([]repository.BookmarkTOCMove, 0, len(refs.Bookmarks))
	keys := map[[2]int32]bool{}
	for _, b := range refs.Bookmarks {
		ch, err := p.target(b.ChapterIndex)
		if err != nil {
			return nil, nil, err
		}
		key := [2]int32{*ch.ChapterIndex, b.CharOffset}
		if keys[key] {
			return nil, nil, ErrRelocationFailed
		}
		keys[key] = true
		moves = append(moves, repository.BookmarkTOCMove{ID: b.ID, From: b.ChapterIndex, To: *ch.ChapterIndex, Offset: b.CharOffset})
	}
	var progress repository.ReadingProgress
	var ch repository.Chapter
	var err error
	if refs.Progress != nil {
		progress = *refs.Progress
		ch, err = p.target(progress.ChapterIndex)
	} else {
		var ok bool
		ch, ok = p.next[0]
		if !ok {
			err = ErrRelocationFailed
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: protected chapter URL missing or ambiguous", ErrRelocationFailed)
	}
	progress.ChapterIndex = *ch.ChapterIndex
	progress.ChapterTitle = ch.Title
	progress.NormalizedTitle = ch.NormalizedTitle
	progress.ProgressPercent = math.Min(100, math.Max(0, (float64(progress.ChapterIndex)+progress.ChapterRatio)/float64(max(p.total, 1))*100))
	return &progress, moves, nil
}
