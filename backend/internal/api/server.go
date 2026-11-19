package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/service"
)

// Deps 是构造 API 服务的依赖集合。
type Deps struct {
	Auth          *service.AuthService
	Sources       *service.SourceService
	Debug         *service.DebugService
	Search        *service.SearchService
	Books         *service.BookService
	Progress      *service.ProgressService
	Mapping       *service.MappingService
	Overrides     *service.OverrideService
	TOC           *service.TOCService
	Content       *service.ContentService
	Import        LocalImporter
	ImportTimeout time.Duration
	Settings      *service.SettingsService
	Bookmarks     *service.BookmarkService
	ReplaceRules  *service.ReplaceRuleService
	Jobs          *service.JobService
	Storage       storage.Provider
	DBPing        func(context.Context) error
	RedisPing     func(context.Context) error
	SchemaPing    func(context.Context) error
	SandboxPing   func(context.Context) error
	Log           *slog.Logger
}

// Server 实现全部已冻结的 OpenAPI 操作。
type Server struct {
	Unimplemented

	Auth          *service.AuthService
	Sources       *service.SourceService
	Debug         *service.DebugService
	Search        *service.SearchService
	Books         *service.BookService
	Progress      *service.ProgressService
	Mapping       *service.MappingService
	Overrides     *service.OverrideService
	TOC           *service.TOCService
	Content       *service.ContentService
	Import        LocalImporter
	importTimeout time.Duration
	Settings      *service.SettingsService
	Bookmarks     *service.BookmarkService
	ReplaceRules  *service.ReplaceRuleService
	Jobs          *service.JobService
	Storage       storage.Provider

	dbPing      func(context.Context) error
	redisPing   func(context.Context) error
	schemaPing  func(context.Context) error
	sandboxPing func(context.Context) error
	log         *slog.Logger
}

// NewServer 构造 API Server。
func NewServer(d Deps) *Server {
	if d.ImportTimeout <= 0 {
		d.ImportTimeout = 90 * time.Second
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		Auth:          d.Auth,
		Sources:       d.Sources,
		Debug:         d.Debug,
		Search:        d.Search,
		Books:         d.Books,
		Progress:      d.Progress,
		Mapping:       d.Mapping,
		Overrides:     d.Overrides,
		TOC:           d.TOC,
		Content:       d.Content,
		Import:        d.Import,
		importTimeout: d.ImportTimeout,
		Settings:      d.Settings,
		Bookmarks:     d.Bookmarks,
		ReplaceRules:  d.ReplaceRules,
		Jobs:          d.Jobs,
		Storage:       d.Storage,
		dbPing:        d.DBPing,
		redisPing:     d.RedisPing,
		schemaPing:    d.SchemaPing,
		sandboxPing:   d.SandboxPing,
		log:           log,
	}
}

// Handler 组装公开 HTTP 中间件与路由：受保护探针与 /api/v1 OpenAPI 路由。
// /metrics 与 /debug/pprof 不再挂载到公开路由，由 OpsHandler 仅在回环/内网地址暴露。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(securityHeaders)
	r.Use(RequestID)
	r.Use(Recover(s.log))
	r.Use(Logging(s.log))

	r.Group(func(protected chi.Router) {
		protected.Use(s.AuthMiddleware)
		protected.Use(IdempotencyKeyValidation(s.log))

		// 容器编排探针（docker-compose / k8s 用根路径）。
		protected.Get("/healthz", s.Healthz)
		protected.Get("/readyz", s.Readyz)

		HandlerWithOptions(s, ChiServerOptions{
			BaseURL:    "/api/v1",
			BaseRouter: protected,
			// 缺失 If-Match（PUT /books/{id}/progress）按契约返回 428 precondition_required，
			// 其余参数绑定错误统一 400 validation_error。
			ErrorHandlerFunc: func(w http.ResponseWriter, req *http.Request, err error) {
				var headerErr *RequiredHeaderError
				if errors.As(err, &headerErr) && strings.EqualFold(headerErr.ParamName, "If-Match") {
					writeErrorCode(w, req, http.StatusPreconditionRequired, "precondition_required")
					return
				}
				writeErrorCode(w, req, http.StatusBadRequest, "validation_error")
			},
		})
	})

	return r
}

// OpsHandler 返回仅用于内网/回环运维端口（OPS_ADDR，默认 127.0.0.1:9090）的路由，
// 承载 /metrics 与按 PPROF_ENABLED=true 启用的 /debug/pprof。
func (s *Server) OpsHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(securityHeaders)
	r.Use(Recover(s.log))
	r.Get("/metrics", promhttp.Handler().ServeHTTP)
	if os.Getenv("PPROF_ENABLED") == "true" {
		r.Mount("/debug", middleware.Profiler())
	}
	return r
}

// Healthz 处理 GET /healthz（存活检查）。
func (s *Server) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz 检查 PG、已配置的 Redis 及应用结构要求；可选依赖不伪造成功。
func (s *Server) Readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]string{"postgres": "fail"}
	status := http.StatusOK
	if s.dbPing == nil || s.dbPing(ctx) != nil {
		status = http.StatusServiceUnavailable
	} else {
		checks["postgres"] = "ok"
	}
	if s.redisPing != nil {
		checks["redis"] = "ok"
		if err := s.redisPing(ctx); err != nil {
			checks["redis"] = "fail"
			status = http.StatusServiceUnavailable
		}
	}
	if s.schemaPing != nil {
		checks["schema"] = "ok"
		if s.schemaPing(ctx) != nil {
			checks["schema"] = "fail"
			status = http.StatusServiceUnavailable
		}
	}
	if s.sandboxPing != nil {
		checks["sandbox"] = "ok"
		if s.sandboxPing(ctx) != nil {
			checks["sandbox"] = "fail"
			status = http.StatusServiceUnavailable
		}
	}
	payload := map[string]any{"status": "ok", "checks": checks}
	if status != http.StatusOK {
		payload["status"] = "degraded"
	}
	writeJSON(w, status, payload)
}

func uuidNil() uuid.UUID { return uuid.Nil }
