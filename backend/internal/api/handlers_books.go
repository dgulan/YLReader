package api

import (
	"net/http"
	"strings"

	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// ListBooks 处理 GET /books（单查询 v_bookshelf 分页 + needs_source 三段式过滤）。
func (s *Server) ListBooks(w http.ResponseWriter, r *http.Request, params ListBooksParams) {
	uid := userIDFrom(r.Context())
	page, pageSize := 1, 20
	if params.Page != nil && *params.Page > 0 {
		page = *params.Page
	}
	if params.PageSize != nil && *params.PageSize > 0 {
		pageSize = *params.PageSize
	}
	if pageSize > 100 {
		pageSize = 100
	}
	f := repository.BookshelfFilter{
		Limit:       pageSize,
		Offset:      (page - 1) * pageSize,
		NeedsSource: params.NeedsSource,
	}
	if params.Q != nil {
		f.Query = strings.TrimSpace(*params.Q)
	}
	if params.Status != nil {
		f.Status = string(*params.Status)
	}

	entries, total, err := s.Books.ShelfList(r.Context(), uid, f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]Book, 0, len(entries))
	for _, e := range entries {
		items = append(items, toAPIBookFromShelf(e))
	}
	totalPages := 0
	if pageSize > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	tp := totalPages
	writeJSON(w, http.StatusOK, BookPage{
		Items:      items,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: &tp,
	})
}

// AddBook 处理 POST /books（201 新建 / 200 已存在并追加备用源）。
func (s *Server) AddBook(w http.ResponseWriter, r *http.Request, _ AddBookParams) {
	uid := userIDFrom(r.Context())
	var req BookCreate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	author := ""
	if req.Author != nil {
		author = *req.Author
	}
	book, status, err := s.Books.Create(r.Context(), uid, service.CreateBookInput{
		Name:     name,
		Author:   author,
		SourceID: req.SourceId,
		BookURL:  req.BookUrl,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, status, toAPIBook(book, nil))
}

// GetBook 处理 GET /books/{bookId}（附当前进度）。
func (s *Server) GetBook(w http.ResponseWriter, r *http.Request, bookId BookId) {
	uid := userIDFrom(r.Context())
	book, prog, err := s.Books.Get(r.Context(), uid, bookId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIBook(book, prog))
}

// UpdateBook 处理 PATCH /books/{bookId}（version 乐观锁；无 SET version）。
func (s *Server) UpdateBook(w http.ResponseWriter, r *http.Request, bookId BookId) {
	uid := userIDFrom(r.Context())
	var req BookUpdate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if req.Version <= 0 {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	patch := repository.BookPatch{
		Name:           req.Name,
		Author:         req.Author,
		CustomCoverURL: req.CustomCoverUrl,
		Intro:          req.Intro,
	}
	if req.Status != nil {
		st := repository.BookStatus(string(*req.Status))
		patch.Status = &st
	}
	book, err := s.Books.Update(r.Context(), uid, bookId, patch, int64(req.Version))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIBook(book, nil))
}

// DeleteBook 处理 DELETE /books/{bookId}（成功 204，不存在 404）。
func (s *Server) DeleteBook(w http.ResponseWriter, r *http.Request, bookId BookId) {
	uid := userIDFrom(r.Context())
	if err := s.Books.Delete(r.Context(), uid, bookId); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toAPIBook 把 repository.Book 映射为 OpenAPI Book。
func toAPIBook(b *repository.Book, p *repository.ReadingProgress) Book {
	out := Book{
		Id:                 b.ID,
		Name:               b.Name,
		Author:             b.Author,
		BookType:           BookBookType(b.BookType),
		Status:             BookStatus(b.Status),
		CoverUrl:           b.CoverURL,
		CustomCoverUrl:     b.CustomCoverURL,
		Intro:              b.Intro,
		Kind:               b.Kind,
		ActiveSourceId:     b.ActiveSourceID,
		ActiveBookUrl:      b.ActiveBookURL,
		TotalChapters:      int(b.TotalChapters),
		LatestChapterTitle: b.LatestChapterTitle,
		LatestChapterAt:    b.LatestChapterAt,
		LastReadAt:         b.LastReadAt,
		CreatedAt:          b.CreatedAt,
		UpdatedAt:          b.UpdatedAt,
		Version:            int(b.Version),
		FileHash:           b.FileHash,
	}
	if b.WordCount != nil {
		wc := int(*b.WordCount)
		out.WordCount = &wc
	}
	if p != nil {
		prog := toAPIProgress(p)
		out.Progress = &prog
	}
	return out
}

// toAPIBookFromShelf 把 v_bookshelf 行映射为 OpenAPI Book（含 needs_source / override_count / progress）。
func toAPIBookFromShelf(e *repository.BookshelfEntry) Book {
	needs := e.NeedsSource
	oc := int(e.OverrideCount)
	out := Book{
		Id:                 e.ID,
		Name:               e.Name,
		Author:             e.Author,
		BookType:           BookBookType(e.BookType),
		Status:             BookStatus(e.Status),
		CoverUrl:           e.CoverURL,
		ActiveSourceId:     e.ActiveSourceID,
		ActiveBookUrl:      e.ActiveBookURL,
		TotalChapters:      int(e.TotalChapters),
		LatestChapterTitle: e.LatestChapterTitle,
		LastReadAt:         e.LastReadAt,
		CreatedAt:          e.CreatedAt,
		UpdatedAt:          e.UpdatedAt,
		Version:            int(e.Version),
		NeedsSource:        &needs,
		OverrideCount:      &oc,
	}
	if e.ProgressChapterIndex != nil {
		prog := Progress{
			BookId:          e.ID,
			ChapterIndex:    int(*e.ProgressChapterIndex),
			ChapterTitle:    e.ProgressChapterTitle,
			ProgressPercent: e.ProgressPercent,
			ServerUpdatedAt: e.ProgressUpdatedAt,
		}
		if e.ProgressCharOffset != nil {
			prog.CharOffset = int(*e.ProgressCharOffset)
		}
		if e.ProgressVersion != nil {
			prog.Version = int(*e.ProgressVersion)
		}
		if e.ProgressRatio != nil {
			prog.ChapterRatio = *e.ProgressRatio
		}
		out.Progress = &prog
	}
	return out
}

// toAPIProgress 把 repository.ReadingProgress 映射为 OpenAPI Progress。
func toAPIProgress(p *repository.ReadingProgress) Progress {
	out := Progress{
		BookId:          p.BookID,
		ChapterIndex:    int(p.ChapterIndex),
		ChapterRatio:    p.ChapterRatio,
		CharOffset:      int(p.CharOffset),
		ClientUpdatedAt: &p.ClientUpdatedAt,
		DeviceId:        p.DeviceID,
		ProgressPercent: &p.ProgressPercent,
		ServerUpdatedAt: &p.ServerUpdatedAt,
		Version:         int(p.Version),
	}
	if p.ChapterTitle != "" {
		t := p.ChapterTitle
		out.ChapterTitle = &t
	}
	if p.NormalizedTitle != "" {
		t := p.NormalizedTitle
		out.NormalizedTitle = &t
	}
	return out
}
