package api

import "net/http"

// SwitchSource 处理 POST /books/{bookId}/switch-source（dry_run 时整体回滚）。
func (s *Server) SwitchSource(w http.ResponseWriter, r *http.Request, bookId BookId) {
	uid := userIDFrom(r.Context())
	var req SwitchSourceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	dryRun := req.DryRun != nil && *req.DryRun
	res, err := s.Mapping.Switch(r.Context(), uid, bookId, req.SourceId, req.BookUrl, dryRun)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := SwitchSourceResult{
		BookId:       res.BookID,
		SourceId:     res.SourceID,
		Strategy:     SwitchSourceResultStrategy(res.Strategy),
		Confidence:   res.Confidence,
		ChapterIndex: int(res.ChapterIndex),
		ChapterRatio: res.ChapterRatio,
		DryRun:       res.DryRun,
	}
	writeJSON(w, http.StatusOK, out)
}
