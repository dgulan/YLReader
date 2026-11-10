package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yreader/backend/internal/repository"
)

// GetProgress 处理 GET /books/{bookId}/progress（无记录返回 0 值 + version=1）。
func (s *Server) GetProgress(w http.ResponseWriter, r *http.Request, bookId BookId) {
	uid := userIDFrom(r.Context())
	p, err := s.Progress.Get(r.Context(), uid, bookId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIProgress(p))
}

// UpdateProgress 处理 PUT /books/{bookId}/progress：If-Match 必需（428），
// 版本冲突返回 409 ProgressConflict（含 current/incoming）。
func (s *Server) UpdateProgress(w http.ResponseWriter, r *http.Request, bookId BookId, params UpdateProgressParams) {
	uid := userIDFrom(r.Context())
	var req ProgressUpdate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	raw := strings.TrimSpace(params.IfMatch)
	if raw == "" {
		writeErrorCode(w, r, http.StatusPreconditionRequired, "precondition_required")
		return
	}
	expected, perr := parseIfMatch(raw)
	if perr != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}

	entity := &repository.ReadingProgress{
		UserID:          uid,
		BookID:          bookId,
		ChapterIndex:    int32(req.ChapterIndex),
		CharOffset:      int32(intOrZero(req.CharOffset)),
		ClientUpdatedAt: time.Now().UTC(),
		DeviceID:        req.DeviceId,
	}
	if req.ChapterTitle != nil {
		entity.ChapterTitle = *req.ChapterTitle
	}
	if req.NormalizedTitle != nil {
		entity.NormalizedTitle = *req.NormalizedTitle
	}
	if req.ChapterRatio != nil {
		entity.ChapterRatio = *req.ChapterRatio
		entity.ProgressPercent = *req.ChapterRatio * 100
	}
	if req.ClientUpdatedAt != nil {
		entity.ClientUpdatedAt = *req.ClientUpdatedAt
	}

	updated, err := s.Progress.Update(r.Context(), uid, entity, expected)
	if err != nil {
		if errors.Is(err, repository.ErrVersionConflict) {
			cur, cerr := s.Progress.Get(r.Context(), uid, bookId)
			if cerr != nil {
				writeError(w, r, cerr)
				return
			}
			writeProgressConflict(w, r, cur, req)
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIProgress(updated))
}

// parseIfMatch 解析 `If-Match: "3"`（允许裸数字）。
func parseIfMatch(raw string) (int64, error) {
	raw = strings.Trim(strings.TrimSpace(raw), `"`)
	return strconv.ParseInt(raw, 10, 64)
}

func intOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// writeProgressConflict 输出 409 ProgressConflict（含 current/incoming 双方进度）。
func writeProgressConflict(w http.ResponseWriter, r *http.Request, cur *repository.ReadingProgress, incoming ProgressUpdate) {
	var resp ProgressConflict
	resp.Error.Code = ProgressConflictErrorCode("progress_conflict")
	resp.Error.Message = messageForCode("progress_conflict")
	if id := requestIDFrom(r.Context()); id != "" {
		resp.Error.RequestId = &id
	}
	resp.Error.Details = &struct {
		Current  Progress       `json:"current"`
		Incoming ProgressUpdate `json:"incoming"`
	}{Current: toAPIProgress(cur), Incoming: incoming}
	writeJSON(w, http.StatusConflict, resp)
}
