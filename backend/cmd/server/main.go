// Package main 是 YReader 后端 Web API 入口：装配 PG 连接池、认证、书源服务、
// 引擎（fetcher/sandbox/parser）与 HTTP 路由。
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/pkg/cache"
	"yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	importTimeout, err := parseImportTimeout(os.Getenv("LOCAL_IMPORT_TIMEOUT"))
	if err != nil {
		log.Error("invalid import timeout", "error", err)
		return err
	}

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Error("connect postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Error("ping postgres", "error", err)
		os.Exit(1)
	}
	schemaCtx, schemaCancel := context.WithTimeout(ctx, 5*time.Second)
	err = repository.CheckSchema(schemaCtx, pool)
	schemaCancel()
	if err != nil {
		log.Error("database schema is not ready; migration required", "error", err)
		return err
	}

	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		if os.Getenv("ENVIRONMENT") == "production" {
			log.Error("JWT_SECRET is required in production")
			os.Exit(1)
		}
		log.Warn("JWT_SECRET not set; using insecure development fallback")
	}

	// v1.4（ADR-010）：书源 header 信封加密。配置了密钥就必须可解（否则 fail-fast），
	// 未配置则在开发环境降级为明文并告警；生产中必须配置，避免误以为已加密。
	ring, err := crypto.LoadKeyringFromEnv()
	switch {
	case err == nil:
		crypto.SetDefaultKeyring(ring)
		log.Info("book source header encryption enabled", "kid", ring.CurrentKID())
	case errors.Is(err, crypto.ErrNoKey):
		if os.Getenv("ENVIRONMENT") == "production" {
			log.Error("YREADER_HEADER_KEY_<KID> is required in production")
			os.Exit(1)
		}
		log.Warn("book source header encryption disabled; set YREADER_HEADER_KEY_<KID> to enable")
	default:
		log.Error("load header keyring", "error", err)
		os.Exit(1)
	}

	authSvc := service.NewAuthService(pool, []byte(secret), service.DefaultAccessTTL, 30*24*time.Hour)

	seedUser := strings.TrimSpace(os.Getenv("SEED_USERNAME"))
	seedPass := os.Getenv("SEED_PASSWORD")
	if seedUser != "" && seedPass != "" {
		created, err := authSvc.EnsureSeedUser(ctx, seedUser, seedPass)
		if err != nil {
			log.Error("seed user", "error", err)
		} else if created {
			log.Info("seed user created", "username", seedUser)
		}
	} else {
		log.Warn("SEED_USERNAME/SEED_PASSWORD not set; no seed user will be created")
	}

	ft := fetcher.New(fetcher.Config{})
	sb := sandbox.New(sandbox.Config{}, sandboxFetcher{ft: ft})

	// 缓存与就绪探针共享一个 Redis 客户端，避免双配置探测错位。
	var contentCache cache.Cache
	redisCache, err := cache.ConfiguredRedis(os.Getenv("REDIS_URL"), os.Getenv("REDIS_ADDR"), time.Second)
	if err != nil {
		log.Error("invalid Redis configuration", "error", err)
		return err
	}
	var redisPing func(context.Context) error
	if redisCache != nil {
		contentCache = redisCache
		redisPing = redisCache.Ping
	} else {
		log.Warn("Redis not configured; content cache falls back to in-memory and readiness omits Redis")
		contentCache = cache.NewMemory()
	}

	// 本地书库文件存储：STORAGE_DIR 必填（Docker 里挂卷），否则本地书正文无处落盘。
	storageDir := strings.TrimSpace(os.Getenv("STORAGE_DIR"))
	if storageDir == "" {
		storageDir = filepath.Join(os.TempDir(), "yreader-storage")
		log.Warn("STORAGE_DIR not set; using temp dir", "dir", storageDir)
	}
	storageProvider, err := storage.NewLocalFS(storageDir)
	if err != nil {
		log.Error("init storage", "error", err)
		os.Exit(1)
	}

	// 后台异步任务：WorkerPool 以 DB 为权威队列（FOR UPDATE SKIP LOCKED），
	// Notify 仅降低延迟；shutdownCtx 由信号驱动，退出时 30s 内等待在跑任务收尾。
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	contentSvc := service.NewContentService(pool, ft, sb, contentCache, storageProvider, log)
	wp := service.NewWorkerPool(pool, 4, log)
	jobSvc := service.NewJobService(pool, contentSvc, wp, log)
	wp.SetExecuteFunc(jobSvc.ExecuteJob)
	wp.Start(shutdownCtx)
	defer func() {
		shutdownTimeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		wp.Shutdown(shutdownTimeout)
	}()

	srv := api.NewServer(api.Deps{
		Auth:          authSvc,
		Sources:       service.NewSourceService(pool),
		Debug:         service.NewDebugService(pool, ft, sb),
		Search:        service.NewSearchService(pool, ft, sb),
		Books:         service.NewBookService(pool, storageProvider, log),
		Progress:      service.NewProgressService(pool),
		Mapping:       service.NewMappingService(pool),
		Overrides:     service.NewOverrideService(pool),
		TOC:           service.NewTOCService(pool, ft, sb),
		Content:       contentSvc,
		Import:        service.NewImportService(pool, storageProvider, log),
		ImportTimeout: importTimeout,
		Settings:      service.NewSettingsService(pool, log),
		Bookmarks:     service.NewBookmarkService(pool, log),
		ReplaceRules:  service.NewReplaceRuleService(pool, log),
		Jobs:          jobSvc,
		Storage:       storageProvider,
		DBPing:        func(c context.Context) error { return pool.Ping(c) },
		RedisPing:     redisPing,
		SchemaPing:    func(c context.Context) error { return repository.CheckSchema(c, pool) },
		Log:           log,
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("yreader backend listening", "port", port)
	if err := serveUntilCanceled(shutdownCtx, httpSrv); err != nil {
		log.Error("server exited", "error", err)
		return err
	}
	return nil
}

// Wait for Shutdown to finish draining requests before releasing service dependencies.
func serveUntilCanceled(ctx context.Context, srv *http.Server) error {
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			<-done
			return err
		}
		<-done
		return nil
	}
}

// sandboxFetcher 把 fetcher.Fetcher 适配为 sandbox.HTTPClient（java.ajax/get/post）。
type sandboxFetcher struct {
	ft *fetcher.Fetcher
}

func (s sandboxFetcher) Do(ctx context.Context, method, rawURL string, body []byte) ([]byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	resp, err := s.ft.Fetch(ctx, method, rawURL, nil, reader)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
