//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/migration"
	"yreader/backend/internal/pkg/cache"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/service"
)

type httpFixtureCredentials struct {
	secret   []byte
	names    [2]string
	old      [2]*service.TokenPair
	password string
}

func fixtureSecret(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		t.Fatal("fixture entropy unavailable")
	}
	t.Cleanup(func() { clear(b) })
	return b
}

func newIsolatedTargetPool(t *testing.T, ctx context.Context, raw string) *pgxpool.Pool {
	t.Helper()
	targetCfg, e := ParseTarget(raw)
	if e != nil {
		t.Fatal("target config rejected")
	}
	poolCfg, e := pgxpool.ParseConfig(raw)
	if e != nil {
		t.Fatal("pool config rejected")
	}
	poolCfg.ConnConfig = targetCfg
	pool, e := pgxpool.NewWithConfig(ctx, poolCfg)
	if e != nil {
		t.Fatal("isolated target pool unavailable")
	}
	return pool
}

// Only the harness-owned synthetic source is writable here; never accept ambient DSNs.
func seedHTTPFixture(t *testing.T, raw string) *httpFixtureCredentials {
	t.Helper()
	cfg, e := ParseTarget(raw)
	if e != nil || cfg.Host != "127.0.0.1" || cfg.Port != 5432 || cfg.User != "postgres" || cfg.Database != "yreader_a1_iso_header_source" {
		t.Fatal("dedicated HTTP fixture required")
	}
	ctx := context.Background()
	pool := newIsolatedTargetPool(t, ctx, raw)
	defer pool.Close()
	f := &httpFixtureCredentials{secret: fixtureSecret(t), password: "Synthetic-restore-only-中文-42!"}
	hash, e := service.HashPassword(f.password)
	if e != nil {
		t.Fatal("fixture password hash")
	}
	if _, e = pool.Exec(ctx, `DELETE FROM auth_sessions`); e != nil {
		t.Fatal("fixture session reset")
	}
	auth := service.NewAuthService(pool, f.secret, time.Hour, time.Hour)
	for i := range f.names {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		if e = pool.QueryRow(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2 RETURNING username`, hash, id).Scan(&f.names[i]); e != nil {
			t.Fatal("fixture password setup")
		}
		f.old[i], e = auth.Login(ctx, f.names[i], f.password)
		if e != nil {
			t.Fatal("fixture source login")
		}
	}
	return f
}

type forbiddenHTTPFetcher struct{ calls atomic.Int32 }

func (f *forbiddenHTTPFetcher) Get(context.Context, string, http.Header) (*fetcher.Response, error) {
	f.calls.Add(1)
	return nil, errors.New("external fetching forbidden in restore HTTP fixture")
}

func assertRestoreHTTP(t *testing.T, g *restoreGuard, stage *stagedObjects, owner string, source *pgx.Conn, c Config, p Plan, raw string, ring *headercrypto.Keyring, f *httpFixtureCredentials) {
	t.Helper()
	ctx := context.Background()
	receipt, _, e := (publicationStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
	if e != nil || receipt.State != "verified" {
		t.Fatal("HTTP requires verified publication")
	}
	if os.Getenv("BACKUP_BEHAVIOR_ACCOUNTS") == "1" {
		assertRestoredBehaviorAccounts(t, g, stage, p, owner, raw, ring, source, f)
		if t.Failed() {
			return
		}
	}
	pool := newIsolatedTargetPool(t, ctx, raw)
	defer pool.Close()
	provider, e := storage.NewLocalFS(filepath.Join(c.Target.Storage.Root, c.Target.Storage.Namespace, receipt.Output))
	if e != nil {
		t.Fatal("HTTP published root")
	}
	// Dedicated, separate processes at fixed harness-only endpoints. No env fallback.
	if os.Getenv("BACKUP_HTTP_SOURCE_REDIS_URL") != "redis://127.0.0.1:16379/0" || os.Getenv("BACKUP_HTTP_TARGET_REDIS_URL") != "redis://127.0.0.1:16380/0" {
		t.Fatal("isolated Redis pair required")
	}
	sourceCache, e := cache.ConfiguredRedis(os.Getenv("BACKUP_HTTP_SOURCE_REDIS_URL"), "", time.Second)
	if e != nil || sourceCache == nil {
		t.Fatal("explicit source cache required")
	}
	targetCache, e := cache.ConfiguredRedis(os.Getenv("BACKUP_HTTP_TARGET_REDIS_URL"), "", time.Second)
	if e != nil || targetCache == nil {
		t.Fatal("explicit target cache required")
	}
	key := "restore-http:" + p.OperationID
	if e = sourceCache.SetEX(ctx, key, []byte("source-only"), time.Minute); e != nil {
		t.Fatal("source Redis unavailable")
	}
	if _, e = targetCache.Get(ctx, key); !errors.Is(e, cache.ErrMiss) {
		t.Fatal("target sees source cache")
	}
	if e = targetCache.SetEX(ctx, key, []byte("target-only"), time.Minute); e != nil {
		t.Fatal("target Redis unavailable")
	}
	value, e := sourceCache.Get(ctx, key)
	if e != nil || string(value) != "source-only" {
		t.Fatal("target overwrote source cache")
	}
	value, e = targetCache.Get(ctx, key)
	if e != nil || string(value) != "target-only" {
		t.Fatal("target cache not independent")
	}
	secret := fixtureSecret(t)
	if bytes.Equal(secret, f.secret) {
		t.Fatal("target JWT secret reused")
	}
	behavior := os.Getenv("BACKUP_HANDOVER_BEHAVIOR") == "1"
	var draft handoverReview
	var registrationProbes handoverProbes
	var releaseRegistrationProbes func()
	var grant behaviorGrant
	var authorization behaviorAuthorization
	var behaviorStart time.Time
	issued := []string{}
	if behavior {
		t.Run("behavior_draft_eight_checks", func(t *testing.T) {
			draft, registrationProbes, releaseRegistrationProbes, _ = fixtureBehaviorDraft(t, g, stage, p, owner, raw, ring, source, f.secret, secret)
		})
		if releaseRegistrationProbes != nil {
			t.Cleanup(releaseRegistrationProbes)
		}
		if t.Failed() {
			return
		}
		authorization = behaviorAuthorization{profile: "isolated-http-fixture-v1", id: uuid.NewString()}
		for i, name := range f.names {
			authorization.accounts = append(authorization.accounts, behaviorAccount{f.old[i].User.ID, name, f.password, f.old[i].AccessToken, f.old[i].RefreshToken})
		}
		t.Run("behavior_reject_bad_credentials", func(t *testing.T) {
			bad := authorization
			bad.accounts = append([]behaviorAccount(nil), authorization.accounts...)
			bad.accounts[0].Password = "wrong"
			if _, e := validateBehaviorAccounts(ctx, pool, f.secret, secret, bad); e != errHandoverBehavior {
				t.Fatal("bad password preflight accepted")
			}
		})
		t.Run("behavior_account_preflight", func(t *testing.T) {
			var e error
			grant, e = validateBehaviorAccounts(ctx, pool, f.secret, secret, authorization)
			if e != nil {
				t.Fatal(e)
			}
		})
		if t.Failed() {
			return
		}
		behaviorStart = time.Now().UTC().Truncate(time.Microsecond)
	}
	oldRing := headercrypto.DefaultKeyring()
	headercrypto.SetDefaultKeyring(ring)
	defer headercrypto.SetDefaultKeyring(oldRing)
	ft := &forbiddenHTTPFetcher{}
	sb := sandbox.New(sandbox.Config{ScriptTimeout: time.Second}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := api.Deps{
		Auth:      service.NewAuthService(pool, secret, time.Minute, time.Hour),
		Books:     service.NewBookService(pool, provider, logger),
		Sources:   service.NewSourceService(pool),
		TOC:       service.NewTOCService(pool, ft, sb),
		Content:   service.NewContentService(pool, ft, sb, targetCache, provider, logger),
		Progress:  service.NewProgressService(pool),
		Bookmarks: service.NewBookmarkService(pool, logger),
		Storage:   provider, DBPing: pool.Ping, RedisPing: targetCache.Ping,
		SchemaPing: func(ctx context.Context) error { return migration.Check(ctx, pool) }, Log: logger,
	}
	// No WorkerPool or scheduler is started by this fixture.
	ts := httptest.NewServer(api.NewServer(deps).Handler())
	defer ts.Close()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, token string, body any, want int) []byte {
		t.Helper()
		var data []byte
		if body != nil {
			data, e = json.Marshal(body)
			if e != nil {
				t.Fatal("fixture request JSON")
			}
		}
		r, e := http.NewRequest(method, ts.URL+path, bytes.NewReader(data))
		if e != nil {
			t.Fatal("fixture request")
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal("fixture HTTP unavailable")
		}
		defer response.Body.Close()
		data, e = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if e != nil || len(data) > 1<<20 || response.StatusCode != want {
			t.Fatalf("HTTP %s %s: status %d, expected %d", method, path, response.StatusCode, want)
		}
		if behavior && want == 200 && method == "POST" && (path == "/api/v1/auth/login" || path == "/api/v1/auth/refresh") {
			var pair api.TokensResponse
			if json.Unmarshal(data, &pair) != nil || pair.RefreshToken == "" {
				t.Fatal("fixture session tracking")
			}
			issued = append(issued, pair.RefreshToken)
		}
		return data
	}
	request("GET", "/api/v1/healthz", "", nil, 200)
	ready := request("GET", "/api/v1/readyz", "", nil, 200)
	var readiness struct {
		Checks map[string]string `json:"checks"`
	}
	if json.Unmarshal(ready, &readiness) != nil || len(readiness.Checks) != 3 || readiness.Checks["postgres"] != "ok" || readiness.Checks["redis"] != "ok" || readiness.Checks["schema"] != "ok" {
		t.Fatal("HTTP readiness omitted dependencies")
	}
	for _, old := range f.old {
		request("GET", "/api/v1/auth/me", old.AccessToken, nil, 401)
		request("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": old.RefreshToken}, 401)
		if _, _, e = service.NewAuthService(pool, f.secret, time.Minute, time.Hour).Authenticate(ctx, old.AccessToken); !errors.Is(e, service.ErrUnauthorized) {
			t.Fatal("token_version did not revoke old JWT")
		}
	}
	request("POST", "/api/v1/auth/login", "", map[string]string{"username": f.names[0], "password": "wrong"}, 401)
	for i, name := range f.names {
		data := request("POST", "/api/v1/auth/login", "", map[string]string{"username": name, "password": f.password}, 200)
		var tokens api.TokensResponse
		if json.Unmarshal(data, &tokens) != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" {
			t.Fatal("target login tokens missing")
		}
		request("GET", "/api/v1/auth/me", tokens.AccessToken, nil, 200)
		book := fmt.Sprintf("20000000-0000-4000-8000-%012d", i+1)
		other := fmt.Sprintf("20000000-0000-4000-8000-%012d", 2-i)
		var shelf api.BookPage
		data = request("GET", "/api/v1/books", tokens.AccessToken, nil, 200)
		if json.Unmarshal(data, &shelf) != nil || len(shelf.Items) != 1 || shelf.Items[0].Id.String() != book {
			t.Fatal("restored bookshelf or ownership incorrect")
		}
		var chapters api.ChapterList
		data = request("GET", "/api/v1/books/"+book+"/chapters", tokens.AccessToken, nil, 200)
		if json.Unmarshal(data, &chapters) != nil || len(chapters.Items) != 1 || chapters.Items[0].ChapterIndex == nil || *chapters.Items[0].ChapterIndex != 0 {
			t.Fatal("restored chapters incorrect")
		}
		var content api.ChapterContent
		data = request("GET", "/api/v1/books/"+book+"/content?chapter_index=0", tokens.AccessToken, nil, 200)
		if json.Unmarshal(data, &content) != nil || content.Cached || !strings.Contains(content.Content, fmt.Sprintf("恢复正文 %d 中文 😀", i+1)) {
			t.Fatal("restored local content incorrect")
		}
		var progress api.Progress
		data = request("GET", "/api/v1/books/"+book+"/progress", tokens.AccessToken, nil, 200)
		if json.Unmarshal(data, &progress) != nil || progress.CharOffset != 17 || progress.Version != 7 {
			t.Fatal("restored progress changed")
		}
		var bookmarks []api.Bookmark
		data = request("GET", "/api/v1/books/"+book+"/bookmarks", tokens.AccessToken, nil, 200)
		if json.Unmarshal(data, &bookmarks) != nil || len(bookmarks) != 1 || bookmarks[0].Note == nil || *bookmarks[0].Note != "unicode: 中文 😀" {
			t.Fatal("restored bookmarks incorrect")
		}
		request("GET", "/api/v1/books/"+other+"/content?chapter_index=0", tokens.AccessToken, nil, 404)
		request("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": tokens.RefreshToken}, 200)
	}
	var active int
	concurrentIssued := assertGeminiConcurrentRefresh(t, pool, secret, f.names[0], f.password)
	if behavior {
		issued = append(issued, concurrentIssued...)
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status IN ('queued','running')`).Scan(&active); e != nil || active != 0 || ft.calls.Load() != 0 {
		t.Fatal("restored background work or external fetch executed")
	}
	if behavior {
		sessions := handoverSessionAllowance{users: grant.users, start: behaviorStart}
		auth := service.NewAuthService(pool, secret, time.Minute, time.Hour)
		for _, token := range issued {
			digest := sha256.Sum256([]byte(token))
			var id uuid.UUID
			if pool.QueryRow(ctx, `SELECT id FROM public.auth_sessions WHERE refresh_token_hash=$1`, hex.EncodeToString(digest[:])).Scan(&id) != nil {
				t.Fatal("known validation session missing")
			}
			sessions.ids = append(sessions.ids, id)
			if auth.Logout(ctx, token) != nil {
				t.Fatal("validation logout unconfirmed")
			}
		}
		// Revalidate old credentials after requests, so expiry is never counted as revocation.
		if _, e := validateBehaviorAccounts(ctx, pool, f.secret, secret, authorization); e != nil {
			t.Fatal("postflight old credential invalid", e)
		}
		sessions.end = time.Now().UTC().Truncate(time.Microsecond)
		ts.Close()
		pool.Close()
		s, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
		if e != nil {
			t.Fatal(e)
		}
		proof := handoverBehaviorProof{data: handoverDataProof{p.Digest, s.tokens, s.pub.TreeFingerprint}, grant: grant, sessions: sessions}
		for i := range proof.checks {
			proof.checks[i] = true
		}
		t.Run("behavior_reject_unaccounted_session", func(t *testing.T) {
			bad := proof
			bad.sessions.ids = append([]uuid.UUID(nil), proof.sessions.ids[:len(proof.sessions.ids)-1]...)
			if report, e := g.finalizeHandoverBehavior(ctx, stage, owner, ring, g.verified.manifest.HeaderKIDs, draft, bad, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }); e != errHandoverBehavior || report.Format != "" {
				t.Fatal("unaccounted validation session accepted")
			}
		})
		var complete handoverReview
		t.Run("behavior_finalize_ten_checks", func(t *testing.T) {
			var e error
			complete, e = g.finalizeHandoverBehavior(ctx, stage, owner, ring, g.verified.manifest.HeaderKIDs, draft, proof, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) })
			if e != nil {
				t.Fatal(e)
			}
			if !complete.Expires.Equal(draft.Expires) {
				t.Fatal("behavior renewed report expiry")
			}
			for _, check := range draft.Checks {
				if (check.ID == "jwt_rotation" || check.ID == "runtime_behavior") && check.Status != "unavailable" {
					t.Fatal("finalization mutated draft")
				}
			}
		})
		if t.Failed() {
			return
		}
		t.Run("behavior_report_save", func(t *testing.T) {
			if g.persistFinalHandoverReview(ctx, complete, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }) != nil {
				t.Fatal("complete review save")
			}
			encoded, _ := json.Marshal(complete)
			fmt.Println("RESTORE_BEHAVIOR_REVIEW " + string(encoded))
		})
		if t.Failed() {
			return
		}
		if os.Getenv("BACKUP_HANDOVER_REGISTER") == "1" {
			var reg handoverRegistration
			t.Run("behavior_registration", func(t *testing.T) {
				var e error
				reg, e = g.registerHandoverReview(ctx, stage, owner, ring, g.verified.manifest.HeaderKIDs, registrationProbes, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) })
				if e != nil {
					t.Fatal(e)
				}
				if reg.State != "registered" || reg.Ready || reg.Deployment || reg.BaseGeneration != complete.Generation || reg.NextGeneration != complete.Generation+1 {
					t.Fatal("registration chain wrong")
				}
				encoded, _ := json.Marshal(reg)
				fmt.Println("RESTORE_REVIEW_REGISTRATION " + string(encoded))
			})
			if t.Failed() {
				return
			}
			t.Run("behavior_registration_no_replay", func(t *testing.T) {
				if _, e := g.registerHandoverReview(ctx, stage, owner, ring, g.verified.manifest.HeaderKIDs, registrationProbes, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }); e != errRegistrationUnknown {
					t.Fatal("registration replay accepted")
				}
				op, sha, e := (Control{g.locations.files[3]}).Load(ctx, p.OperationID)
				if e != nil || op.Generation != reg.NextGeneration || op.Phase != "validating" || sha != reg.NextSHA {
					t.Fatal("replay changed state")
				}
			})
		}
	}
	op, _, e := (Control{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
	if e != nil || op.Phase != "validating" {
		t.Fatal("HTTP fixture advanced restore state")
	}
	// Experimental observation only; never a durable state or a cutover authorization.
	evidence := map[string]any{
		"scope": "isolated-http-fixture", "version": 1, "operation_id": p.OperationID,
		"plan_digest": p.Digest, "package_sha256": p.PackageSHA, "manifest_sha256": p.ManifestSHA,
		"target_instance_id": p.TargetID, "target_storage_fingerprint": p.TargetFingerprint,
		"publication_tree_fingerprint": receipt.TreeFingerprint, "publication_state": receipt.State,
		"ready_for_cutover": false, "deployment_authorized": false, "operation_phase": op.Phase,
		"checks": map[string]bool{"health_readiness": true, "fresh_login_refresh": true, "old_tokens_rejected": true, "token_version_revocation": true, "jwt_key_rotated": true, "redis_pair_isolated": true, "bookshelf_chapters_content": true, "progress_bookmarks": true, "tenant_isolation": true, "no_background_or_fetch": true},
	}
	data, e := json.Marshal(evidence)
	if e != nil {
		t.Fatal("evidence encoding")
	}
	fmt.Println("RESTORE_HTTP_EVIDENCE " + string(data))
}
