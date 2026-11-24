package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// ListSources 处理 GET /sources。
func (s *Server) ListSources(w http.ResponseWriter, r *http.Request, params ListSourcesParams) {
	uid := userIDFrom(r.Context())
	page, pageSize := 1, 20
	if params.Page != nil && *params.Page > 0 {
		page = *params.Page
	}
	if params.PageSize != nil && *params.PageSize > 0 {
		pageSize = *params.PageSize
	}
	if pageSize > 100 {
		// 防御：钳制深翻页，避免大 OFFSET 拖库。
		pageSize = 100
	}
	filter := repository.SourceListFilter{
		Group:        params.Group,
		Enabled:      params.Enabled,
		Limit:        pageSize,
		Offset:       (page - 1) * pageSize,
		ExcludeLocal: true, // 本地书库虚拟源不在在线书源列表中展示
	}
	if params.Q != nil {
		filter.Query = *params.Q
	}
	if params.Sort != nil {
		filter.Sort = string(*params.Sort)
	}

	list, total, err := s.Sources.List(r.Context(), uid, filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]Source, 0, len(list))
	for _, src := range list {
		items = append(items, toAPISource(src))
	}
	totalPages := 0
	if pageSize > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	writeJSON(w, http.StatusOK, SourcePage{
		Items:      items,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: &totalPages,
	})
}

// CreateSource 处理 POST /sources。
func (s *Server) CreateSource(w http.ResponseWriter, r *http.Request, _ CreateSourceParams) {
	uid := userIDFrom(r.Context())
	var req SourceCreate
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if err := validateSourceURL(req.BookSourceUrl); err != nil {
		writeError(w, r, err)
		return
	}
	src := &repository.BookSource{
		UserID:          uid,
		BookSourceURL:   strings.TrimSpace(req.BookSourceUrl),
		BookSourceName:  strings.TrimSpace(req.BookSourceName),
		BookSourceGroup: "默认",
		Enabled:         true,
		EnabledExplore:  true,
		Header:          json.RawMessage(`{}`),
		Rules:           json.RawMessage(`{}`),
	}
	if src.BookSourceName == "" {
		src.BookSourceName = src.BookSourceURL
	}
	applySourceCreate(src, req)

	if err := s.Sources.Create(r.Context(), uid, src); err != nil {
		writeError(w, r, err)
		return
	}
	got, err := s.Sources.Get(r.Context(), uid, src.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAPISource(got))
}

// GetSource 处理 GET /sources/{sourceId}。
func (s *Server) GetSource(w http.ResponseWriter, r *http.Request, sourceId SourceId) {
	uid := userIDFrom(r.Context())
	src, err := s.Sources.Get(r.Context(), uid, sourceId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISource(src))
}

// UpdateSource 处理 PUT /sources/{sourceId}（version 乐观锁）。
func (s *Server) UpdateSource(w http.ResponseWriter, r *http.Request, sourceId SourceId) {
	uid := userIDFrom(r.Context())
	var req SourceUpdate
	if err := decodeJSON(w, r, &req, "version"); err != nil || req.Version < 1 {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	cur, err := s.Sources.Get(r.Context(), uid, sourceId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if req.BookSourceUrl != nil {
		if err := validateSourceURL(*req.BookSourceUrl); err != nil {
			writeError(w, r, err)
			return
		}
	}
	applySourceUpdate(cur, req)

	if err := s.Sources.Update(r.Context(), uid, cur, int64(req.Version)); err != nil {
		writeError(w, r, err)
		return
	}
	got, err := s.Sources.Get(r.Context(), uid, sourceId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISource(got))
}

// DeleteSource 处理 DELETE /sources/{sourceId}。
func (s *Server) DeleteSource(w http.ResponseWriter, r *http.Request, sourceId SourceId) {
	uid := userIDFrom(r.Context())
	if err := s.Sources.Delete(r.Context(), uid, sourceId); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ImportSources 处理 POST /sources/import（上限 32 MiB / 5000 条；dry_run 由 Service 回滚）。
func (s *Server) ImportSources(w http.ResponseWriter, r *http.Request, _ ImportSourcesParams) {
	uid := userIDFrom(r.Context())

	// 导入体允许大 JSON：单独放宽到 32 MiB。
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	var req ImportRequest
	if err := decodeBody(r.Body, &req); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeErrorCode(w, r, http.StatusRequestEntityTooLarge, "payload_too_large")
			return
		}
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if req.Format != LegadoJson && req.Format != YreaderExport {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}

	sources, err := parseImportPayload(req.Data)
	if err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if len(sources) > 5000 {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}

	overwrite, ok := importConflictMode((*string)(req.Overwrite), (*string)(req.ConflictMode))
	if !ok {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	dryRun := req.DryRun != nil && *req.DryRun

	rep, err := s.Sources.Import(r.Context(), uid, sources, repository.ImportOptions{
		Overwrite: overwrite,
		DryRun:    dryRun,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}

	failed := make([]ImportFailure, 0, len(rep.Failures))
	for _, f := range rep.Failures {
		url := f.URL
		item := ImportFailure{Index: f.Index, Reason: f.Message}
		if url != "" {
			item.BookSourceUrl = &url
		}
		failed = append(failed, item)
	}
	duplicates := make([]ImportDuplicate, 0, len(rep.Duplicates))
	for _, item := range rep.Duplicates {
		duplicates = append(duplicates, ImportDuplicate{BookSourceUrl: item.URL, BookSourceName: item.Name})
	}
	duplicateCount := len(duplicates)
	writeJSON(w, http.StatusOK, ImportResult{
		ImportedCount:    &rep.Success,
		OverwrittenCount: &rep.Updated,
		DuplicateCount:   &duplicateCount,
		Duplicates:       &duplicates,
		DryRun:           dryRun,
		Imported:         rep.Success,
		Skipped:          rep.Skipped,
		Updated:          rep.Updated,
		Failed:           failed,
	})
}

// ImportSourcesFromUrl 处理 POST /sources/import-url（受 SSRF 防护抓取远程 JSON，复用导入与预检管线）。
func (s *Server) ImportSourcesFromUrl(w http.ResponseWriter, r *http.Request, _ ImportSourcesFromUrlParams) {
	uid := userIDFrom(r.Context())

	var req ImportURLRequest
	if err := decodeJSON(w, r, &req, "url"); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	overwrite, ok := importConflictMode((*string)(req.Overwrite), (*string)(req.ConflictMode))
	if !ok {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if err := validateSourceURL(req.Url); err != nil {
		writeError(w, r, err)
		return
	}

	rawData, err := s.Sources.FetchSourcesFromURL(r.Context(), req.Url)
	if err != nil {
		if errors.Is(err, service.ErrImportPayloadTooLarge) {
			writeErrorCode(w, r, http.StatusRequestEntityTooLarge, "payload_too_large")
			return
		}
		writeError(w, r, err)
		return
	}

	sources, err := parseImportPayload(rawData)
	if err != nil || len(sources) > 5000 {
		writeErrorCode(w, r, http.StatusUnprocessableEntity, "validation_error")
		return
	}

	dryRun := req.DryRun != nil && *req.DryRun

	rep, err := s.Sources.Import(r.Context(), uid, sources, repository.ImportOptions{
		Overwrite: overwrite,
		DryRun:    dryRun,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}

	failed := make([]ImportFailure, 0, len(rep.Failures))
	for _, f := range rep.Failures {
		u := f.URL
		item := ImportFailure{Index: f.Index, Reason: f.Message}
		if u != "" {
			item.BookSourceUrl = &u
		}
		failed = append(failed, item)
	}
	duplicates := make([]ImportDuplicate, 0, len(rep.Duplicates))
	for _, item := range rep.Duplicates {
		duplicates = append(duplicates, ImportDuplicate{BookSourceUrl: item.URL, BookSourceName: item.Name})
	}
	duplicateCount := len(duplicates)
	writeJSON(w, http.StatusOK, ImportResult{
		ImportedCount:    &rep.Success,
		OverwrittenCount: &rep.Updated,
		DuplicateCount:   &duplicateCount,
		Duplicates:       &duplicates,
		DryRun:           dryRun,
		Imported:         rep.Success,
		Skipped:          rep.Skipped,
		Updated:          rep.Updated,
		Failed:           failed,
	})
}

func importConflictMode(legacy, mode *string) (string, bool) {
	if mode == nil {
		if legacy != nil && *legacy == "update" {
			return "update", true
		}
		return "skip", true
	}
	switch *mode {
	case "skip":
		return "skip", true
	case "overwrite":
		return "update", true
	default:
		return "", false
	}
}

// ExportSources 处理 GET /sources/export（Legado 兼容 JSON 数组）。
func (s *Server) ExportSources(w http.ResponseWriter, r *http.Request, params ExportSourcesParams) {
	uid := userIDFrom(r.Context())
	var ids []uuid.UUID
	if params.Ids != nil {
		ids = *params.Ids
	} else if raw := r.URL.Query().Get("ids"); raw != "" {
		// 兜底：部分客户端不按 style=form,explode=false 序列化。
		for _, part := range strings.Split(raw, ",") {
			id, err := uuid.Parse(strings.TrimSpace(part))
			if err != nil {
				writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
				return
			}
			ids = append(ids, id)
		}
	}
	out, err := s.Sources.Export(r.Context(), uid, ids)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
}

func validateSourceURL(raw string) error {
	// 与调试接口 resolveInput 共用同一套预检，避免两条入口规则漂移。
	return service.ValidateSourceURL(raw)
}

func applySourceCreate(dst *repository.BookSource, req SourceCreate) {
	if req.BookSourceGroup != nil && strings.TrimSpace(*req.BookSourceGroup) != "" {
		dst.BookSourceGroup = *req.BookSourceGroup
	}
	if req.BookSourceType != nil {
		dst.BookSourceType = int16(*req.BookSourceType)
	}
	if req.Enabled != nil {
		dst.Enabled = *req.Enabled
	}
	if req.EnabledExplore != nil {
		dst.EnabledExplore = *req.EnabledExplore
	}
	if req.Weight != nil {
		dst.Weight = int32(*req.Weight)
	}
	if req.Charset != nil {
		dst.Charset = req.Charset
	}
	if req.ConcurrentRate != nil {
		dst.ConcurrentRate = req.ConcurrentRate
	}
	if req.LoginUrl != nil {
		dst.LoginURL = req.LoginUrl
	}
	if req.LoginCheckJs != nil {
		dst.LoginCheckJS = req.LoginCheckJs
	}
	if req.Header != nil {
		if b, err := json.Marshal(*req.Header); err == nil {
			dst.Header = b
		}
	}
	if req.Rules != nil {
		if b, err := json.Marshal(req.Rules); err == nil {
			dst.Rules = b
		}
	}
}

func applySourceUpdate(dst *repository.BookSource, req SourceUpdate) {
	if req.BookSourceGroup != nil {
		dst.BookSourceGroup = *req.BookSourceGroup
	}
	if req.BookSourceName != nil && strings.TrimSpace(*req.BookSourceName) != "" {
		dst.BookSourceName = *req.BookSourceName
	}
	if req.BookSourceType != nil {
		dst.BookSourceType = int16(*req.BookSourceType)
	}
	if req.BookSourceUrl != nil {
		dst.BookSourceURL = strings.TrimSpace(*req.BookSourceUrl)
	}
	if req.Enabled != nil {
		dst.Enabled = *req.Enabled
	}
	if req.EnabledExplore != nil {
		dst.EnabledExplore = *req.EnabledExplore
	}
	if req.Weight != nil {
		dst.Weight = int32(*req.Weight)
	}
	if req.Charset != nil {
		dst.Charset = req.Charset
	}
	if req.ConcurrentRate != nil {
		dst.ConcurrentRate = req.ConcurrentRate
	}
	if req.LoginUrl != nil {
		dst.LoginURL = req.LoginUrl
	}
	if req.LoginCheckJs != nil {
		dst.LoginCheckJS = req.LoginCheckJs
	}
	if req.Header != nil {
		if b, err := json.Marshal(*req.Header); err == nil {
			dst.Header = b
		}
	}
	if req.Rules != nil {
		if b, err := json.Marshal(req.Rules); err == nil {
			dst.Rules = b
		}
	}
}

func toAPISource(s *repository.BookSource) Source {
	bt := int(s.BookSourceType)
	ee := s.EnabledExplore
	rt := int(s.RespondTime)
	fc := int(s.FailureCount)
	var header *map[string]interface{}
	if len(s.Header) > 0 {
		var h map[string]interface{}
		if json.Unmarshal(s.Header, &h) == nil {
			header = &h
		}
	}
	var rules SourceRules
	if len(s.Rules) > 0 {
		_ = json.Unmarshal(s.Rules, &rules)
	}
	return Source{
		Id:              s.ID,
		BookSourceUrl:   s.BookSourceURL,
		BookSourceName:  s.BookSourceName,
		BookSourceGroup: s.BookSourceGroup,
		BookSourceType:  &bt,
		Enabled:         s.Enabled,
		EnabledExplore:  &ee,
		Weight:          int(s.Weight),
		RespondTime:     &rt,
		Charset:         s.Charset,
		ConcurrentRate:  s.ConcurrentRate,
		Header:          header,
		LoginUrl:        s.LoginURL,
		LoginCheckJs:    s.LoginCheckJS,
		Rules:           rules,
		FailureCount:    &fc,
		LastCheckedAt:   s.LastCheckedAt,
		LastSuccessAt:   s.LastSuccessAt,
		Version:         int(s.Version),
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
}

// parseImportPayload 解析导入数据：对象数组或单对象。
func parseImportPayload(data string) ([]json.RawMessage, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil, fmt.Errorf("%w: empty data", repository.ErrInvalidArgument)
	}
	var list []json.RawMessage
	if err := json.Unmarshal([]byte(data), &list); err == nil {
		return list, nil
	}
	var single json.RawMessage
	if err := json.Unmarshal([]byte(data), &single); err != nil {
		return nil, err
	}
	if !json.Valid(single) {
		return nil, fmt.Errorf("%w: invalid json", repository.ErrInvalidArgument)
	}
	return []json.RawMessage{single}, nil
}
