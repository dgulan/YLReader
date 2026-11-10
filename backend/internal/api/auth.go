package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"yreader/backend/internal/service"
)

const maxJSONBody = 1 << 20 // 1 MiB，认证类请求体上限

// Login 处理 POST /auth/login。防用户枚举：任何凭据失败统一 401 unauthorized。
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	pair, err := s.Auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		s.writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tokensResponse(pair))
}

// RefreshToken 处理 POST /auth/refresh（轮换 + 复用检测）。
func (s *Server) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req RefreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	pair, err := s.Auth.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		s.writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tokensResponse(pair))
}

// Logout 处理 POST /auth/logout（吊销当前 refresh token，幂等）。
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	var req RefreshRequest
	// 允许空 body：无 refresh token 也返回 204。
	_ = decodeJSON(w, r, &req)
	if err := s.Auth.Logout(r.Context(), req.RefreshToken); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetCurrentUser 处理 GET /auth/me。
func (s *Server) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	uid := userIDFrom(r.Context())
	if uid == uuidNil() {
		writeErrorCode(w, r, http.StatusUnauthorized, "unauthorized")
		return
	}
	u, err := s.Auth.Me(r.Context(), uid)
	if err != nil {
		s.writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIUser(u))
}

// AuthMiddleware 校验 Bearer token（除公开路径外），并把用户写入 context。
func (s *Server) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeErrorCode(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		uid, tv, err := s.Auth.Authenticate(r.Context(), strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			writeErrorCode(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserID, uid)
		ctx = context.WithValue(ctx, ctxTokenVersion, tv)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isPublicPath(path string) bool {
	switch {
	case strings.HasSuffix(path, "/auth/login"),
		strings.HasSuffix(path, "/auth/refresh"),
		strings.HasSuffix(path, "/healthz"),
		strings.HasSuffix(path, "/readyz"):
		return true
	}
	return false
}

func (s *Server) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrInvalidCredentials) ||
		errors.Is(err, service.ErrUnauthorized) ||
		errors.Is(err, service.ErrSessionExpired) {
		writeErrorCode(w, r, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeError(w, r, err)
}

func tokensResponse(p *service.TokenPair) TokensResponse {
	return TokensResponse{
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		ExpiresIn:    p.ExpiresIn,
		TokenType:    Bearer,
		User:         toAPIUser(p.User),
	}
}

func toAPIUser(u *service.User) User {
	return User{
		Id:           u.ID,
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		Role:         UserRole(u.Role),
		TokenVersion: u.TokenVersion,
		CreatedAt:    u.CreatedAt,
		LastLoginAt:  u.LastLoginAt,
	}
}

// decodeJSON 解析请求体 JSON，限制大小。
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	body := http.MaxBytesReader(w, r.Body, maxJSONBody)
	return json.NewDecoder(body).Decode(v)
}
