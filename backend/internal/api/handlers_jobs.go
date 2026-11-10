package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// CacheBook 发起整本预热任务（202）。本地书 / 无活跃源 → 422；同类任务在途 → 409。
// Idempotency-Key 本批次解析但不做强幂等拦截（归档 DEF-OAS-05）。
func (s *Server) CacheBook(w http.ResponseWriter, r *http.Request, bookId BookId, params CacheBookParams) {
	var req CacheBookRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusUnprocessableEntity, "validation_error")
		return
	}

	in := service.CacheBookInput{
		FromChapter: req.FromChapter,
		ToChapter:   req.ToChapter,
	}
	if req.SourceId != nil {
		sid := uuid.UUID(*req.SourceId)
		in.SourceID = &sid
	}

	job, err := s.Jobs.CacheBook(r.Context(), userIDFrom(r.Context()), bookId, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(job))
}

// GetJob 读取任务状态。
func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, jobId BookId) {
	job, err := s.Jobs.Get(r.Context(), userIDFrom(r.Context()), jobId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIJob(job))
}

// CancelJob 取消任务：终态 → 409；未命中 → 404。
func (s *Server) CancelJob(w http.ResponseWriter, r *http.Request, jobId BookId) {
	if err := s.Jobs.Cancel(r.Context(), userIDFrom(r.Context()), jobId); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toAPIJob 把仓储 Job 映射为契约 Job。
func toAPIJob(j *repository.Job) Job {
	out := Job{
		Id:           j.ID,
		Type:         JobType(j.Type),
		Status:       JobStatus(j.Status),
		Progress:     int(j.Progress),
		ErrorCode:    j.ErrorCode,
		ErrorMessage: j.ErrorMessage,
		CreatedAt:    j.CreatedAt,
		StartedAt:    j.StartedAt,
		FinishedAt:   j.FinishedAt,
	}
	if j.Result != nil && len(*j.Result) > 0 {
		var m map[string]any
		if err := json.Unmarshal(*j.Result, &m); err == nil {
			out.Result = &m
		}
	}
	// S5-2：target_source_id 由 payload.source_id 解析，不落 DB 列；仅 full_cache 非空。
	if j.Type == "full_cache" {
		var p struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal(j.Payload, &p); err == nil && p.SourceID != "" {
			if id, perr := uuid.Parse(p.SourceID); perr == nil {
				ts := openapi_types.UUID(id)
				out.TargetSourceId = &ts
			}
		}
	}
	return out
}
