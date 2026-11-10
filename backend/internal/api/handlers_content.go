package api

import (
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"yreader/backend/internal/repository"
)

// GetChapterContent 处理 GET /books/{bookId}/content（operationId getChapterContent）。
//
// M2c 边界：
//   - chapter_index / chapter_id 严格二选一；
//   - source_id 由 active/override 自动决定，显式传入一律 400；
//   - prefetch 属 M3 能力，当前 400。
func (s *Server) GetChapterContent(w http.ResponseWriter, r *http.Request, bookId BookId, params GetChapterContentParams) {
	if params.SourceId != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if params.Prefetch != nil && *params.Prefetch {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if (params.ChapterIndex == nil) == (params.ChapterId == nil) {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if params.ChapterIndex != nil && *params.ChapterIndex < 0 {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if params.ChapterId != nil && *params.ChapterId < 1 {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}

	view, err := s.Content.Get(r.Context(), userIDFrom(r.Context()), uuid.UUID(bookId), params.ChapterIndex, params.ChapterId)
	if err != nil {
		writeError(w, r, err)
		return
	}

	applied := make([]openapi_types.UUID, 0, len(view.AppliedReplaceRuleIDs))
	for _, id := range view.AppliedReplaceRuleIDs {
		applied = append(applied, openapi_types.UUID(id))
	}
	sourceID := openapi_types.UUID(view.SourceID)
	// trace_id 仅在经过抓取管线时存在；本地书必须显式输出 null。
	var traceID *string
	if view.TraceID != "" {
		t := view.TraceID
		traceID = &t
	}
	overridden := view.IsOverridden

	var prev, next *int
	if view.PrevChapterIndex != nil {
		v := int(*view.PrevChapterIndex)
		prev = &v
	}
	if view.NextChapterIndex != nil {
		v := int(*view.NextChapterIndex)
		next = &v
	}

	writeJSON(w, http.StatusOK, ChapterContent{
		Chapter:               toAPIChapterPlain(view.Chapter),
		Content:               view.Content,
		Cached:                view.Cached,
		WordCount:             view.WordCount,
		AppliedReplaceRuleIds: &applied,
		TraceId:               traceID,
		SourceId:              &sourceID,
		IsOverridden:          &overridden,
		PrevChapterIndex:      prev,
		NextChapterIndex:      next,
	})
}

// toAPIChapterPlain 把活跃源逻辑章节转为契约 Chapter；
// 覆盖相关字段保持默认（is_overridden=false、override_*=null），以顶层 ChapterContent 为准。
func toAPIChapterPlain(c *repository.Chapter) Chapter {
	var idx *int
	if c.ChapterIndex != nil {
		v := int(*c.ChapterIndex)
		idx = &v
	}
	notOverridden := false
	out := Chapter{
		ChapterIndex:    idx,
		Id:              c.ID,
		IsOverridden:    &notOverridden,
		IsVolume:        c.IsVolume,
		NormalizedTitle: c.NormalizedTitle,
		SortOrder:       int(c.SortOrder),
		Title:           c.Title,
		Url:             c.URL,
		WordCount:       int(c.WordCount),
		PublishedAt:     c.PublishedAt,
	}
	if c.IsVIP {
		vip := true
		out.IsVip = &vip
	}
	return out
}
