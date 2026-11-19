package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/goleak"

	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/pkg/cache"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/service"
)

const concurrencyWorkers = 100

type syntheticOriginFetcher struct {
	hits  atomic.Int64
	pages map[string]string
}

func (f *syntheticOriginFetcher) Get(_ context.Context, rawURL string, _ http.Header) (*fetcher.Response, error) {
	f.hits.Add(1)
	body, ok := f.pages[rawURL]
	if !ok {
		return nil, fmt.Errorf("%w: unexpected url %s", fetcher.ErrUpstreamError, rawURL)
	}
	return &fetcher.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
		FinalURL:   rawURL,
	}, nil
}

func percentileLatency(samples []time.Duration, q float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), samples...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)-1) * q)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func runConcurrent100(tb testing.TB, fn func(worker int) error) []time.Duration {
	tb.Helper()
	latencies := make([]time.Duration, concurrencyWorkers)
	errs := make([]error, concurrencyWorkers)
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(concurrencyWorkers)
	for i := 0; i < concurrencyWorkers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-startGate
			begin := time.Now()
			errs[i] = fn(i)
			latencies[i] = time.Since(begin)
		}()
	}
	close(startGate)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			tb.Fatalf("worker %d failed: %v", i, err)
		}
	}
	return latencies
}

func openPoolDirect(tb testing.TB, dsn string) *pgxpool.Pool {
	tb.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		tb.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	if cfg.MaxConns < 32 {
		cfg.MaxConns = 32
	}
	cfg.MaxConnIdleTime = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		tb.Fatalf("new pgxpool: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		tb.Fatalf("ping pgxpool: %v", err)
	}
	return pool
}

func newConcurrencyServer(tb testing.TB, pool *pgxpool.Pool, ft service.Fetcher, c cache.Cache) (*httptest.Server, *http.Client, func()) {
	tb.Helper()
	sb := sandbox.New(sandbox.Config{ScriptTimeout: 400 * time.Millisecond}, nil)
	store, err := storage.NewLocalFS(tb.TempDir())
	if err != nil {
		tb.Fatalf("init local storage: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	contentSvc := service.NewContentService(pool, ft, sb, c, store, logger)
	deps := api.Deps{
		Auth:         service.NewAuthService(pool, []byte("r6-concurrency-secret-key-material"), time.Minute, time.Hour),
		Sources:      service.NewSourceService(pool),
		Debug:        service.NewDebugService(pool, ft, sb),
		Search:       service.NewSearchService(pool, ft, sb),
		Books:        service.NewBookService(pool, store, logger),
		Progress:     service.NewProgressService(pool),
		Mapping:      service.NewMappingService(pool),
		Overrides:    service.NewOverrideService(pool).WithContent(contentSvc),
		TOC:          service.NewTOCService(pool, ft, sb),
		Content:      contentSvc,
		Import:       service.NewImportService(pool, store, logger),
		Settings:     service.NewSettingsService(pool, logger),
		Bookmarks:    service.NewBookmarkService(pool, logger),
		ReplaceRules: service.NewReplaceRuleService(pool, logger),
		Storage:      store,
		Log:          logger,
	}
	if pool != nil {
		deps.DBPing = func(ctx context.Context) error { return pool.Ping(ctx) }
	} else {
		deps.DBPing = func(context.Context) error { return nil }
		deps.RedisPing = func(context.Context) error { return nil }
		deps.SchemaPing = func(context.Context) error { return nil }
	}
	srv := api.NewServer(deps)
	ts := httptest.NewServer(srv.Handler())
	tr := &http.Transport{
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     30 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second}
	cleanup := func() {
		client.CloseIdleConnections()
		tr.CloseIdleConnections()
		ts.CloseClientConnections()
		ts.Close()
	}
	return ts, client, cleanup
}

type concurrencyFixture struct {
	pool     *pgxpool.Pool
	ts       *httptest.Server
	client   *http.Client
	fetcher  *syntheticOriginFetcher
	userID   uuid.UUID
	token    string
	bookID   uuid.UUID
	source1  uuid.UUID
	source2  uuid.UUID
	book2URL string
	cleanup  func()
}

func setupIntegrationConcurrencyFixture(tb testing.TB, dsn string) *concurrencyFixture {
	tb.Helper()
	const totalChapters = 20
	pages := make(map[string]string, totalChapters)
	for i := 0; i < totalChapters; i++ {
		pages[fmt.Sprintf("https://bench-s1.example.com/c/%d", i)] = fmt.Sprintf(
			`<html><body><div id="content"><p>第%d章合成回源正文内容，用于100并发缓存命中与穿透基准。</p></div></body></html>`, i)
	}
	ft := &syntheticOriginFetcher{pages: pages}
	pool := openPoolDirect(tb, dsn)
	ts, client, closeHTTP := newConcurrencyServer(tb, pool, ft, cache.NewMemory())

	ctx := context.Background()
	username := "bench100_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	password := "Bench100!Pass"
	hash, err := service.HashPassword(password)
	if err != nil {
		closeHTTP()
		pool.Close()
		tb.Fatalf("hash password: %v", err)
	}
	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
		username, hash).Scan(&userID); err != nil {
		closeHTTP()
		pool.Close()
		tb.Fatalf("seed user: %v", err)
	}

	cleanupAll := func() {
		closeHTTP()
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		pool.Close()
	}

	rules := `{"content":{"content":"div#content@text"}}`
	var s1, s2 uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO book_sources (user_id, book_source_url, book_source_name, book_source_group,
			book_source_type, enabled, enabled_explore, weight, respond_time, header, rules)
		VALUES ($1, $2, 'BenchS1', '默认', 0, true, true, 0, 0, '{}', $3::jsonb)
		RETURNING id`, userID, "https://bench-s1.example.com/"+userID.String(), rules).Scan(&s1); err != nil {
		cleanupAll()
		tb.Fatalf("seed source1: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO book_sources (user_id, book_source_url, book_source_name, book_source_group,
			book_source_type, enabled, enabled_explore, weight, respond_time, header, rules)
		VALUES ($1, $2, 'BenchS2', '默认', 0, true, true, 0, 0, '{}', $3::jsonb)
		RETURNING id`, userID, "https://bench-s2.example.com/"+userID.String(), rules).Scan(&s2); err != nil {
		cleanupAll()
		tb.Fatalf("seed source2: %v", err)
	}

	book1URL := "https://bench-s1.example.com/book/" + userID.String()
	book2URL := "https://bench-s2.example.com/book/" + userID.String()
	var bookID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO books (user_id, book_type, normalized_name, normalized_author, name, author,
			status, active_source_id, active_book_url, total_chapters)
		VALUES ($1, 'online', '并发基准书', '作者', '并发基准书', '作者', 'serializing', $2, $3, $4)
		RETURNING id`, userID, s1, book1URL, totalChapters).Scan(&bookID); err != nil {
		cleanupAll()
		tb.Fatalf("seed book: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO book_source_mappings (book_id, source_id, book_url, name, author, sync_status)
		VALUES ($1, $2, $3, '并发基准书', '作者', 'active'),
		       ($1, $4, $5, '并发基准书', '作者', 'active')`,
		bookID, s1, book1URL, s2, book2URL); err != nil {
		cleanupAll()
		tb.Fatalf("seed mappings: %v", err)
	}

	for i := 0; i < totalChapters; i++ {
		title1 := fmt.Sprintf("第%d章 山间青松翠竹长风", i+1)
		norm1 := fmt.Sprintf("第%d章山间青松翠竹长风", i+1)
		u1 := fmt.Sprintf("https://bench-s1.example.com/c/%d", i)
		if _, err := pool.Exec(ctx, `
			INSERT INTO chapters (book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash)
			VALUES ($1, $2, $3, $3, $4, $5, $6, encode(sha256($7::bytea), 'hex'))`,
			bookID, s1, i, title1, norm1, u1, []byte(u1)); err != nil {
			cleanupAll()
			tb.Fatalf("seed ch1: %v", err)
		}
		// 故意制造轻微后缀差异，触发 pg_trgm 模糊匹配路径（similarity >= 0.6）。
		title2 := fmt.Sprintf("第%d章 山间青松翠竹长风修", i+1)
		norm2 := fmt.Sprintf("第%d章山间青松翠竹长风修", i+1)
		if i == 10 {
			// 在邻近槽位放置候选以验证模糊定位排序。
			title2 = "第11章 山间青松翠竹长风修订版"
			norm2 = "第11章山间青松翠竹长风修订版"
		}
		u2 := fmt.Sprintf("https://bench-s2.example.com/c/%d", i)
		if _, err := pool.Exec(ctx, `
			INSERT INTO chapters (book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash)
			VALUES ($1, $2, $3, $3, $4, $5, $6, encode(sha256($7::bytea), 'hex'))`,
			bookID, s2, i, title2, norm2, u2, []byte(u2)); err != nil {
			cleanupAll()
			tb.Fatalf("seed ch2: %v", err)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO reading_progress (user_id, book_id, chapter_index, char_offset, chapter_ratio, chapter_title, normalized_title)
		VALUES ($1, $2, 10, 12, 0.25, '第11章 山间青松翠竹长风', '第11章山间青松翠竹长风')`,
		userID, bookID); err != nil {
		cleanupAll()
		tb.Fatalf("seed progress: %v", err)
	}

	loginBody, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := client.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		cleanupAll()
		tb.Fatalf("login request: %v", err)
	}
	rawLogin, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cleanupAll()
		tb.Fatalf("login status %d: %s", resp.StatusCode, rawLogin)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rawLogin, &tok); err != nil || tok.AccessToken == "" {
		cleanupAll()
		tb.Fatalf("decode login token: %v", err)
	}

	return &concurrencyFixture{
		pool:     pool,
		ts:       ts,
		client:   client,
		fetcher:  ft,
		userID:   userID,
		token:    tok.AccessToken,
		bookID:   bookID,
		source1:  s1,
		source2:  s2,
		book2URL: book2URL,
		cleanup:  cleanupAll,
	}
}

func trigramSimilarity(a, b string) float64 {
	grams := func(s string) map[string]struct{} {
		r := []rune("  " + s + " ")
		out := make(map[string]struct{}, len(r))
		for i := 0; i+3 <= len(r); i++ {
			out[string(r[i:i+3])] = struct{}{}
		}
		return out
	}
	ga, gb := grams(a), grams(b)
	if len(ga) == 0 && len(gb) == 0 {
		return 1
	}
	inter := 0
	for k := range ga {
		if _, ok := gb[k]; ok {
			inter++
		}
	}
	union := len(ga) + len(gb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// TestConcurrencyReadAndRelocate_NoGoroutineLeak 验证 100 并发章节读取（合成回源与缓存命中）、
// 100 并发换源定位，并在显式关闭 HTTP Server 与 pgxpool 后通过 goleak 零泄漏校验。
func TestConcurrencyReadAndRelocate_NoGoroutineLeak(t *testing.T) {
	// 必须在最外层先注册 goleak，保证内层连接池与 HTTP Server 先关闭再校验。
	defer goleak.VerifyNone(t)

	if dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")); dsn != "" {
		fix := setupIntegrationConcurrencyFixture(t, dsn)
		defer fix.cleanup()

		// 1. 100 并发合成回源章节读取（冷启动回源 + 写入缓存）。
		missLatencies := runConcurrent100(t, func(worker int) error {
			chIdx := worker % 20
			req, err := http.NewRequest(http.MethodGet,
				fmt.Sprintf("%s/api/v1/books/%s/content?chapter_index=%d", fix.ts.URL, fix.bookID, chIdx), nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+fix.token)
			resp, err := fix.client.Do(req)
			if err != nil {
				return err
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("content miss status=%d body=%s", resp.StatusCode, raw)
			}
			var out struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return err
			}
			if !strings.Contains(out.Content, "合成回源正文内容") {
				return fmt.Errorf("unexpected content: %q", out.Content)
			}
			return nil
		})
		missP95 := percentileLatency(missLatencies, 0.95)
		hitsAfterCold := fix.fetcher.hits.Load()
		t.Logf("[integration] 100-concurrency content fetch (cold/fill): p95=%v, upstream_hits=%d", missP95, hitsAfterCold)

		// 2. 100 并发缓存命中章节读取（0 新增回源）。
		hitLatencies := runConcurrent100(t, func(worker int) error {
			chIdx := worker % 20
			req, err := http.NewRequest(http.MethodGet,
				fmt.Sprintf("%s/api/v1/books/%s/content?chapter_index=%d", fix.ts.URL, fix.bookID, chIdx), nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+fix.token)
			resp, err := fix.client.Do(req)
			if err != nil {
				return err
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("content cached status=%d body=%s", resp.StatusCode, raw)
			}
			var out struct {
				Cached  bool   `json:"cached"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return err
			}
			if !out.Cached {
				return fmt.Errorf("expected cached=true for ch=%d", chIdx)
			}
			return nil
		})
		hitP95 := percentileLatency(hitLatencies, 0.95)
		if fix.fetcher.hits.Load() != hitsAfterCold {
			t.Fatalf("cache hit phase triggered upstream fetch: before=%d after=%d", hitsAfterCold, fix.fetcher.hits.Load())
		}
		t.Logf("[integration] 100-concurrency content cache hit: p95=%v", hitP95)

		// 3. 100 并发换源干跑定位（每请求独立从 *pgxpool.Pool 获取连接与事务，禁止共享 pgx.Tx）。
		switchPayload, _ := json.Marshal(map[string]any{
			"source_id": fix.source2.String(),
			"book_url":  fix.book2URL,
			"dry_run":   true,
		})
		relocLatencies := runConcurrent100(t, func(_ int) error {
			req, err := http.NewRequest(http.MethodPost,
				fmt.Sprintf("%s/api/v1/books/%s/switch-source", fix.ts.URL, fix.bookID),
				bytes.NewReader(switchPayload))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+fix.token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := fix.client.Do(req)
			if err != nil {
				return err
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("switch-source status=%d body=%s", resp.StatusCode, raw)
			}
			var out struct {
				Strategy     string  `json:"strategy"`
				ChapterIndex int     `json:"chapter_index"`
				Confidence   float64 `json:"confidence"`
				DryRun       bool    `json:"dry_run"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return err
			}
			if !out.DryRun || out.ChapterIndex != 10 || out.Strategy != "fuzzy" || out.Confidence < 0.6 {
				return fmt.Errorf("unexpected switch result: %+v", out)
			}
			return nil
		})
		relocP95 := percentileLatency(relocLatencies, 0.95)
		t.Logf("[integration] 100-concurrency switch-source fuzzy relocation: p95=%v", relocP95)
		return
	}

	// 无 TEST_DATABASE_URL 时的内存/合成回源并发与 goleak 校验模式（直接驱动 Handler，不依赖回环 TCP）。
	const chaptersCount = 20
	pages := make(map[string]string, chaptersCount)
	for i := 0; i < chaptersCount; i++ {
		pages[fmt.Sprintf("https://mock-origin.example.com/c/%d", i)] = fmt.Sprintf(
			`<html><body><div id="content"><p>第%d章内存合成正文内容</p></div></body></html>`, i)
	}
	ft := &syntheticOriginFetcher{pages: pages}
	memCache := cache.NewMemory()
	sb := sandbox.New(sandbox.Config{ScriptTimeout: 400 * time.Millisecond}, nil)
	store, err := storage.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("init local storage: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.NewServer(api.Deps{
		Content:    service.NewContentService(nil, ft, sb, memCache, store, logger),
		Storage:    store,
		DBPing:     func(context.Context) error { return nil },
		RedisPing:  func(context.Context) error { return nil },
		SchemaPing: func(context.Context) error { return nil },
		Log:        logger,
	})
	handler := srv.Handler()

	ctx := context.Background()
	missLatencies := runConcurrent100(t, func(worker int) error {
		chIdx := worker % chaptersCount
		u := fmt.Sprintf("https://mock-origin.example.com/c/%d", chIdx)
		resp, err := ft.Get(ctx, u, nil)
		if err != nil {
			return err
		}
		node, err := parser.NewHTMLDocumentNode(string(resp.Body))
		if err != nil {
			return err
		}
		text, err := parser.New(sb).EvalField(ctx, node, "div#content@text")
		if err != nil {
			return err
		}
		if !strings.Contains(text, "内存合成正文内容") {
			return fmt.Errorf("unexpected parsed text %q", text)
		}
		return memCache.SetEX(ctx, fmt.Sprintf("yreader:cache:content:mock:%d", chIdx), []byte(text), time.Minute)
	})

	hitLatencies := runConcurrent100(t, func(worker int) error {
		chIdx := worker % chaptersCount
		val, err := memCache.Get(ctx, fmt.Sprintf("yreader:cache:content:mock:%d", chIdx))
		if err != nil || !strings.Contains(string(val), "内存合成正文内容") {
			return fmt.Errorf("cache miss for %d: err=%v", chIdx, err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("readyz status=%d", rec.Code)
		}
		return nil
	})

	candidates := make([]string, 200)
	for i := range candidates {
		candidates[i] = fmt.Sprintf("第%d章 篇章序号%03d卷风雪夜归人", i+1, i+1)
	}
	candidates[10] = "第11章 篇章序号011卷风雪夜归人修订版"
	relocLatencies := runConcurrent100(t, func(_ int) error {
		target := "第11章 篇章序号011卷风雪夜归人"
		bestIdx, bestScore := -1, 0.0
		for i, c := range candidates {
			if score := trigramSimilarity(target, c); score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx != 10 || bestScore < 0.6 {
			return fmt.Errorf("fuzzy match got idx=%d score=%.3f", bestIdx, bestScore)
		}
		return nil
	})

	t.Logf("[in-memory] 100-concurrency p95: fetch+parse=%v, cache+http=%v, fuzzy-relocate=%v",
		percentileLatency(missLatencies, 0.95),
		percentileLatency(hitLatencies, 0.95),
		percentileLatency(relocLatencies, 0.95))
}

func BenchmarkConcurrencyContentRead100(b *testing.B) {
	if dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")); dsn != "" {
		fix := setupIntegrationConcurrencyFixture(b, dsn)
		defer fix.cleanup()

		// 预热第 0 章缓存
		req, _ := http.NewRequest(http.MethodGet,
			fmt.Sprintf("%s/api/v1/books/%s/content?chapter_index=0", fix.ts.URL, fix.bookID), nil)
		req.Header.Set("Authorization", "Bearer "+fix.token)
		resp, err := fix.client.Do(req)
		if err != nil {
			b.Fatalf("warmup: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		var mu sync.Mutex
		lat := make([]time.Duration, 0, 256)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			samples := runConcurrent100(b, func(_ int) error {
				r, err := http.NewRequest(http.MethodGet,
					fmt.Sprintf("%s/api/v1/books/%s/content?chapter_index=0", fix.ts.URL, fix.bookID), nil)
				if err != nil {
					return err
				}
				r.Header.Set("Authorization", "Bearer "+fix.token)
				res, err := fix.client.Do(r)
				if err != nil {
					return err
				}
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != http.StatusOK {
					return fmt.Errorf("status=%d", res.StatusCode)
				}
				return nil
			})
			mu.Lock()
			lat = append(lat, samples...)
			mu.Unlock()
		}
		b.StopTimer()
		b.ReportMetric(float64(percentileLatency(lat, 0.95).Microseconds())/1000.0, "p95_ms")
		return
	}

	memCache := cache.NewMemory()
	ctx := context.Background()
	_ = memCache.SetEX(ctx, "yreader:cache:content:bench:0", []byte("基准缓存正文"), time.Minute)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := api.NewServer(api.Deps{
		DBPing:     func(context.Context) error { return nil },
		RedisPing:  func(context.Context) error { return nil },
		SchemaPing: func(context.Context) error { return nil },
		Log:        logger,
	}).Handler()

	lat := make([]time.Duration, 0, 256)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		samples := runConcurrent100(b, func(_ int) error {
			if _, err := memCache.Get(ctx, "yreader:cache:content:bench:0"); err != nil {
				return err
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				return fmt.Errorf("status=%d", rec.Code)
			}
			return nil
		})
		lat = append(lat, samples...)
	}
	b.StopTimer()
	b.ReportMetric(float64(percentileLatency(lat, 0.95).Microseconds())/1000.0, "p95_ms")
}
