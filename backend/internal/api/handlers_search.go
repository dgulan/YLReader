package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// SearchBooks 处理 GET /search（10s 预算 + 多源并发 + 部分失败隔离）。
func (s *Server) SearchBooks(w http.ResponseWriter, r *http.Request, params SearchBooksParams) {
	uid := userIDFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var ids []uuid.UUID
	if params.SourceIds != nil {
		ids = *params.SourceIds
	}
	page, pageSize := 1, 20
	if params.Page != nil {
		page = *params.Page
	}
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}

	out, err := s.Search.Search(ctx, uid, params.Keyword, ids, page, pageSize)
	if err != nil {
		writeError(w, r, err)
		return
	}

	resp := SearchResponse{
		Keyword:  params.Keyword,
		Groups:   make([]SearchGroup, 0, len(out.Groups)),
		Failures: make([]SearchFailure, 0, len(out.Failures)),
	}
	for _, f := range out.Failures {
		_, code := mapErrorToCode(f.Err)
		resp.Failures = append(resp.Failures, SearchFailure{
			SourceId: f.SourceID,
			Code:     code,
			Message:  messageForCode(code),
		})
	}

	total := 0
	for _, g := range out.Groups {
		group := SearchGroup{
			SourceId:   g.SourceID,
			SourceName: g.SourceName,
			Items:      make([]SearchBook, 0, len(g.Items)),
		}
		elapsed := g.ElapsedMs
		group.ElapsedMs = &elapsed
		for _, it := range g.Items {
			group.Items = append(group.Items, SearchBook{
				Name:          it.Name,
				Author:        optString(it.Author),
				BookUrl:       it.BookURL,
				CoverUrl:      optString(it.CoverURL),
				Intro:         optString(it.Intro),
				Kind:          optString(it.Kind),
				LatestChapter: optString(it.LatestChapter),
				WordCount:     it.WordCount,
				SourceId:      g.SourceID,
			})
		}
		total += len(group.Items)
		resp.Groups = append(resp.Groups, group)
	}
	// total = 所有成功源截断后贡献的书籍总和；失败源不计入。
	resp.Total = total
	writeJSON(w, http.StatusOK, resp)
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
