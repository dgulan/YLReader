package api

import (
	"net/http"

	"github.com/google/uuid"

	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// ListChapters 处理 GET /books/{bookId}/chapters（operationId listChapters）。
// 租户隔离、source_id 预览模式与 refresh 语义由 service 层保证；
// 抓取失败按错误类型透传 504/502/422，绝不吞成 200 空目录。
func (s *Server) ListChapters(w http.ResponseWriter, r *http.Request, bookId BookId, params ListChaptersParams) {
	var sourceID *uuid.UUID
	if params.SourceId != nil {
		id := uuid.UUID(*params.SourceId)
		sourceID = &id
	}
	refresh := params.Refresh != nil && *params.Refresh

	view, err := s.TOC.GetChapters(r.Context(), userIDFrom(r.Context()), uuid.UUID(bookId), sourceID, refresh)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIChapterList(view))
}

func toAPIChapterList(v *service.ChapterListView) ChapterList {
	items := make([]Chapter, 0, len(v.Items))
	for _, c := range v.Items {
		items = append(items, toAPIChapter(c))
	}
	return ChapterList{
		BookId:      v.BookID,
		Items:       items,
		RefreshedAt: v.RefreshedAt,
		SourceId:    v.SourceID,
		Total:       v.Total,
	}
}

// toAPIChapter 转换目录行；卷名行 chapter_index 必须为 null（指针 nil），严禁置 0。
func toAPIChapter(c *repository.TOCChapter) Chapter {
	var idx *int
	if c.ChapterIndex != nil {
		v := int(*c.ChapterIndex)
		idx = &v
	}
	out := Chapter{
		ChapterIndex:    idx,
		Id:              c.ID,
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
	overridden := c.IsOverridden
	out.IsOverridden = &overridden
	out.OverrideSourceId = c.OverrideSourceID
	out.OverrideSourceName = c.OverrideSourceName
	out.OverrideUrl = c.OverrideURL
	if c.OverrideStatus != nil {
		status := ChapterOverrideStatus(*c.OverrideStatus)
		out.OverrideStatus = &status
	}
	return out
}
