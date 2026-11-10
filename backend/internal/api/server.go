package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/service"
)

// Deps 是构造 API 服务的依赖集合。
type Deps struct {
	Auth         *service.AuthService
	Sources      *service.SourceService
	Debug        *service.DebugService
	Search       *service.SearchService
	Books        *service.BookService
	Progress     *service.ProgressService
	Mapping      *service.MappingService
	Overrides    *service.OverrideService
	TOC          *service.TOCService
	Content      *service.ContentService
	Import       *service.ImportService
	Settings     *service.SettingsService
	Bookmarks    *service.BookmarkService
	ReplaceRules *service.ReplaceRuleService
	Jobs         *service.JobService
	Storage      storage.Provider
	DBPing       func(context.Context) error
	RedisPing    func(context.Context) error
	Log          *slog.Logger
}

// Server 实现 OpenAPI 生成的 ServerInterface：auth/sources/search/debug/healthz/readyz
// 与 M2a 书架/进度/换源/单章覆盖真实实现，其余端点沿用 Unimplemented 的 501。
type Server struct {
	Unimplemented

	Auth         *service.AuthService
	Sources      *service.SourceService
	Debug        *service.DebugService
	Search       *service.SearchService
	Books        *service.BookService
	Progress     *service.ProgressService
	Mapping      *service.MappingService
	Overrides    *service.OverrideService
	TOC          *service.TOCService
	Content      *service.ContentService
	Import       *service.ImportService
	Settings     *service.SettingsService
	Bookmarks    *service.BookmarkService
	ReplaceRules *service.ReplaceRuleService
	Jobs         *service.JobService
	Storage      storage.Provider

	dbPing    func(context.Context) error
	redisPing func(context.Context) error
	log       *slog.Logger
}

// NewServer 构造 API Server。
func NewServer(d Deps) *Server {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		Auth:         d.Auth,
		Sources:      d.Sources,
		Debug:        d.Debug,
		Search:       d.Search,
		Books:        d.Books,
		Progress:     d.Progress,
		Mapping:      d.Mapping,
		Overrides:    d.Overrides,
		TOC:          d.TOC,
		Content:      d.Content,
		Import:       d.Import,
		Settings:     d.Settings,
		Bookmarks:    d.Bookmarks,
		ReplaceRules: d.ReplaceRules,
		Jobs:         d.Jobs,
		Storage:      d.Storage,
		dbPing:       d.DBPing,
		redisPing:    d.RedisPing,
		log:          log,
	}
}

// Handler 组装中间件与路由：根路径探针 + /api/v1 OpenAPI 路由。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recover(s.log))
	r.Use(Logging(s.log))

	// S5-4：/metrics 与 /debug/pprof 注册在受保护路由组之前（不挂 AuthMiddleware），
	// 免 JWT 鉴权。/metrics 由反代限制内网访问；pprof 由 PPROF_ENABLED 控制（默认关闭）。
	// chi 要求所有中间件先于路由定义，故这里用 Group 分出「免鉴权」与「受保护」两组。
	r.Get("/metrics", promhttp.Handler().ServeHTTP)
	if os.Getenv("PPROF_ENABLED") == "true" {
		r.Mount("/debug", middleware.Profiler())
	}

	r.Group(func(protected chi.Router) {
		protected.Use(s.AuthMiddleware)

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

// Healthz 处理 GET /healthz（存活检查）。
func (s *Server) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz 处理 GET /readyz（PG 必查；配置 REDIS_URL 时同步查 Redis）。
func (s *Server) Readyz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{"postgres": "ok", "redis": "ok"}
	status := http.StatusOK
	if s.dbPing != nil {
		if err := s.dbPing(r.Context()); err != nil {
			checks["postgres"] = "error"
			status = http.StatusServiceUnavailable
		}
	}
	if s.redisPing != nil {
		if err := s.redisPing(r.Context()); err != nil {
			checks["redis"] = "error"
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
