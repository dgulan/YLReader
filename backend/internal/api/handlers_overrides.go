package api

import (
	"net/http"

	"yreader/backend/internal/repository"
)

// SetChapterOverride 处理 POST /books/{bookId}/chapters/{chapterIndex}/override。
func (s *Server) SetChapterOverride(w http.ResponseWriter, r *http.Request, bookId BookId, chapterIndex int) {
	uid := userIDFrom(r.Context())
	if chapterIndex < 0 {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	var req ChapterOverrideCreate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	ov, src, err := s.Overrides.Set(r.Context(), uid, bookId, int32(chapterIndex), req.ChapterUrl, req.SourceId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIOverride(ov, src))
}

// DeleteChapterOverride 处理 DELETE .../override（幂等：无论是否存在都返回 204）。
func (s *Server) DeleteChapterOverride(w http.ResponseWriter, r *http.Request, bookId BookId, chapterIndex int) {
	uid := userIDFrom(r.Context())
	if chapterIndex < 0 {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if err := s.Overrides.Delete(r.Context(), uid, bookId, int32(chapterIndex)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toAPIOverride 映射单章覆盖响应（M2a 不做正文试抓，content_preview 为空串）。
func toAPIOverride(o *repository.ChapterSourceOverride, src *repository.BookSource) ChapterOverride {
	out := ChapterOverride{
		BookId:         o.BookID,
		ChapterIndex:   int(o.ChapterIndex),
		ChapterTitle:   o.ChapterTitle,
		ChapterUrl:     o.ChapterURL,
		ContentPreview: "",
		CreatedAt:      o.CreatedAt,
		SourceId:       o.SourceID,
		SourceName:     src.BookSourceName,
		Status:         OverrideStatus(string(o.Status)),
		UpdatedAt:      o.UpdatedAt,
	}
	return out
}
