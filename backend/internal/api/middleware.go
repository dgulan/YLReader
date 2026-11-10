package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ctxKey 是本包私有的 context key 类型，避免与其它包冲突。
type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxUserID
	ctxTokenVersion
)

// requestIDFrom 从 context 取请求 ID（不存在返回空串）。
func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

// userIDFrom 从 context 取已认证用户 ID（未认证返回 uuid.Nil）。
func userIDFrom(ctx context.Context) uuid.UUID {
	if v, ok := ctx.Value(ctxUserID).(uuid.UUID); ok {
		return v
	}
	return uuid.Nil
}

// RequestID 生成或透传 X-Request-Id，并写入响应头与 context。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = "req_" + uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// Recover 捕获 handler panic，返回 500 internal_error，避免进程崩溃。
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered", "panic", rec, "path", r.URL.Path)
					writeErrorCode(w, r, http.StatusInternalServerError, "internal_error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusWriter 记录响应状态码供日志使用。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Logging 输出结构化访问日志（X-Request-Id 已由 RequestID 注入）。
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", requestIDFrom(r.Context()),
			)
		})
	}
}
