package api

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// ListBookmarks 返回某书的书签列表。
func (s *Server) ListBookmarks(w http.ResponseWriter, r *http.Request, bookId BookId) {
	uid := userIDFrom(r.Context())
	items, err := s.Bookmarks.List(r.Context(), uid, uuid.UUID(bookId))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIBookmarks(items))
}

// CreateBookmark 新建书签；书不存在/越权返回 400，重复位置返回 409。
func (s *Server) CreateBookmark(w http.ResponseWriter, r *http.Request, bookId BookId) {
	uid := userIDFrom(r.Context())
	var req BookmarkCreate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	in := service.CreateBookmarkInput{
		ChapterIndex: int32(req.ChapterIndex),
		ChapterTitle: req.ChapterTitle,
		Excerpt:      req.Excerpt,
		Note:         req.Note,
	}
	if req.CharOffset != nil {
		in.CharOffset = int32(*req.CharOffset)
	}
	b, err := s.Bookmarks.Create(r.Context(), uid, uuid.UUID(bookId), in)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicate) {
			writeErrorCode(w, r, http.StatusConflict, "conflict")
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAPIBookmark(b))
}

// DeleteBookmark 删除书签（路径不带 bookId）。
func (s *Server) DeleteBookmark(w http.ResponseWriter, r *http.Request, bookmarkId BookId) {
	uid := userIDFrom(r.Context())
	if err := s.Bookmarks.Delete(r.Context(), uid, uuid.UUID(bookmarkId)); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeErrorCode(w, r, http.StatusNotFound, "not_found")
			return
		}
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toAPIBookmarks(items []*repository.Bookmark) []Bookmark {
	out := make([]Bookmark, 0, len(items))
	for _, b := range items {
		out = append(out, *toAPIBookmark(b))
	}
	return out
}

func toAPIBookmark(b *repository.Bookmark) *Bookmark {
	var title *string
	if b.ChapterTitle != "" {
		title = &b.ChapterTitle
	}
	return &Bookmark{
		Id:           b.ID,
		BookId:       b.BookID,
		ChapterIndex: int(b.ChapterIndex),
		CharOffset:   int(b.CharOffset),
		ChapterTitle: title,
		Excerpt:      b.Excerpt,
		Note:         b.Note,
		CreatedAt:    b.CreatedAt,
	}
}
