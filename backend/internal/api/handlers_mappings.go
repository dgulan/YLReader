package api

import (
	"errors"
	"net/http"

	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// ListMappings 返回书的多源绑定（空集合返回 []，严禁 null）。
func (s *Server) ListMappings(w http.ResponseWriter, r *http.Request, bookId BookId) {
	items, err := s.Mapping.List(r.Context(), userIDFrom(r.Context()), bookId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]BookSourceMapping, 0, len(items))
	for _, m := range items {
		out = append(out, toAPIBookSourceMapping(m, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

// AddMapping 新增备用源绑定：相同 (book, source, url) → 409；源不可用 → 422。
func (s *Server) AddMapping(w http.ResponseWriter, r *http.Request, bookId BookId) {
	var req MappingCreate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusUnprocessableEntity, "validation_error")
		return
	}
	m, err := s.Mapping.Add(r.Context(), userIDFrom(r.Context()), bookId,
		req.SourceId, req.BookUrl)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAPIBookSourceMapping(m, nil))
}

// DeleteMapping 删除备用源绑定；活跃源绑定 → 409，未命中 → 404。
func (s *Server) DeleteMapping(w http.ResponseWriter, r *http.Request, bookId BookId, mappingId BookId) {
	err := s.Mapping.Delete(r.Context(), userIDFrom(r.Context()), bookId, mappingId)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeErrorCode(w, r, http.StatusNotFound, "not_found")
			return
		}
		if errors.Is(err, service.ErrMappingActive) {
			writeErrorCode(w, r, http.StatusConflict, "conflict")
			return
		}
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toAPIBookSourceMapping 把仓储模型映射为契约模型；sourceName 可选（列表接口不带源名）。
func toAPIBookSourceMapping(m *repository.BookSourceMapping, sourceName *string) BookSourceMapping {
	out := BookSourceMapping{
		Id:                 m.ID,
		BookId:             m.BookID,
		SourceId:           m.SourceID,
		BookUrl:            m.BookURL,
		TocUrl:             m.TOCURL,
		Name:               m.Name,
		Author:             m.Author,
		LatestChapterTitle: m.LatestChapterTitle,
		SyncStatus:         BookSourceMappingSyncStatus(m.SyncStatus),
		LastSyncedAt:       m.LastSyncedAt,
		SourceName:         sourceName,
	}
	total := int(m.TotalChapters)
	out.TotalChapters = &total
	return out
}
