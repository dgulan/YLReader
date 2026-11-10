package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"yreader/backend/internal/service"
)

// GetSettings 返回用户全部设置（扁平 key/value）。
func (s *Server) GetSettings(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r.Context())
	values, err := s.Settings.Get(r.Context(), uid)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISettings(values))
}

// PatchSettings 浅合并设置；值为 null 表示删除该 key。
func (s *Server) PatchSettings(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r.Context())
	var patch map[string]json.RawMessage
	if err := decodeJSON(w, r, &patch); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	values, err := s.Settings.Patch(r.Context(), uid, patch)
	if err != nil {
		if errors.Is(err, service.ErrInvalidSettingsKey) {
			writeErrorCode(w, r, http.StatusUnprocessableEntity, "validation_error")
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISettings(values))
}

// toAPISettings 把原始 JSON 值解码为契约要求的 map[string]interface{}。
func toAPISettings(values map[string]json.RawMessage) Settings {
	out := make(Settings, len(values))
	for key, raw := range values {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		out[key] = v
	}
	return out
}
