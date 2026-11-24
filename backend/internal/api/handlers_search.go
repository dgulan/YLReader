package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"yreader/backend/internal/service"
)

// SearchBooks aggregates source results until the request is canceled.
func (s *Server) SearchBooks(w http.ResponseWriter, r *http.Request, params SearchBooksParams) {
	uid := userIDFrom(r.Context())

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

	var onGroup func(service.GroupResult)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream := strings.TrimSpace(r.Header.Get("Accept")) == "application/x-ndjson"
	controller := http.NewResponseController(w)
	encoder := json.NewEncoder(w)
	writeEvent := func(event searchEvent) {
		if ctx.Err() != nil {
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Cache-Control", "no-store")
		if err := encoder.Encode(event); err != nil {
			cancel()
			return
		}
		if err := controller.Flush(); err != nil {
			cancel()
		}
	}
	w.Header().Add("Vary", "Accept")
	if stream {
		onGroup = func(g service.GroupResult) {
			group := searchGroup(g)
			writeEvent(searchEvent{Type: "group", Group: &group})
		}
	}
	out, err := s.Search.SearchProgress(ctx, uid, params.Keyword, ids, page, pageSize, onGroup)
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
			Message:  sourceFailureMessage(f.Err, code),
		})
	}

	for _, g := range out.Groups {
		group := searchGroup(g)
		resp.Total += len(group.Items)
		resp.Groups = append(resp.Groups, group)
	}
	if stream {
		writeEvent(searchEvent{Type: "done", Result: &resp})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type searchEvent struct {
	Type   string          `json:"type"`
	Group  *SearchGroup    `json:"group,omitempty"`
	Result *SearchResponse `json:"result,omitempty"`
}

func searchGroup(g service.GroupResult) SearchGroup {
	group := SearchGroup{
		SourceId:   g.SourceID,
		SourceName: g.SourceName,
		SourceHost: sourceHostname(g.SourceURL),
		Items:      make([]SearchBook, 0, len(g.Items)),
		ElapsedMs:  &g.ElapsedMs,
	}
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
	return group
}

func sourceHostname(rawURL string) *string {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil
	}
	return optString(parsed.Hostname())
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
