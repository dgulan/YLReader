//go:build integration && linux

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/sandbox"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/service"
)

func isAllowedReaderHTTPRequest(r *http.Request) bool {
	if r.Method != http.MethodGet || r.ContentLength != 0 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	if r.URL.Path == "/api/v1/books" && r.URL.RawQuery == "" {
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/books/") {
		return false
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/chapters") && r.URL.RawQuery == "":
		return true
	case strings.HasSuffix(r.URL.Path, "/progress") && r.URL.RawQuery == "":
		return true
	case strings.HasSuffix(r.URL.Path, "/bookmarks") && r.URL.RawQuery == "":
		return true
	case strings.HasSuffix(r.URL.Path, "/content") && strings.HasPrefix(r.URL.RawQuery, "chapter_index="):
		return true
	}
	return false
}

func assertBehaviorHTTPDispatch(t *testing.T, g *restoreGuard, stage *stagedObjects, p Plan, owner, raw, mode string, ring *headercrypto.Keyring, source *pgx.Conn, f *httpFixtureCredentials) {
	t.Helper()
	ctx := context.Background()
	clock := func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
	draft, _, release, inputs := fixtureBehaviorDraft(t, g, stage, p, owner, raw, ring, source, f.secret, fixtureSecret(t))
	defer release()
	s, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	a := behaviorConsent{Format: "ylreader-behavior-consent", Version: 1, Scope: "isolated-account-verification-only", ID: uuid.NewString(), OperationID: p.OperationID, OwnerID: owner, PlanDigest: p.Digest, TargetID: p.TargetID, Generation: s.op.Generation, OperationSHA: s.tokens[0], AttemptSHA: s.tokens[1], PublicationSHA: s.tokens[2], RuntimeSHA: draft.TargetIdentity.Runtime, ReaderScope: behaviorReaderScopeReadOnly, Issued: clock(), Expires: clock().Add(time.Minute)}
	data := map[string][]byte{}
	users := []uuid.UUID{}
	for i, name := range f.names {
		key := fmt.Sprintf("account-%d.json", i)
		id := f.old[i].User.ID
		users = append(users, id)
		a.Accounts = append(a.Accounts, behaviorConsentAccount{id.String(), key})
		data[key], _ = json.Marshal(map[string]string{"user_id": id.String(), "username": name, "password": f.password, "old_access_token": f.old[i].AccessToken, "old_refresh_token": f.old[i].RefreshToken})
	}
	data["consent.json"], _ = json.Marshal(a)
	prepared, e := prepareBehaviorInput(ctx, &diagnosticReader{data: data}, g.locations.files[3], "consent.json", p, a.RuntimeSHA, clock)
	if e != nil {
		t.Fatal(e)
	}
	defer prepared.Close()
	prePool := newIsolatedTargetPool(t, ctx, raw)
	defer prePool.Close()
	postPool := newIsolatedTargetPool(t, ctx, raw)
	defer postPool.Close()
	oldRing := headercrypto.DefaultKeyring()
	headercrypto.SetDefaultKeyring(ring)
	defer headercrypto.SetDefaultKeyring(oldRing)
	receipt, _, e := (publicationStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
	if e != nil || receipt.State != "verified" {
		t.Fatal("publication receipt required", e)
	}
	provider, e := storage.NewLocalFS(filepath.Join(g.config.Target.Storage.Root, g.config.Target.Storage.Namespace, receipt.Output))
	if e != nil {
		t.Fatal("published localfs root unavailable", e)
	}
	ft := &forbiddenHTTPFetcher{}
	sb := sandbox.New(sandbox.Config{ScriptTimeout: time.Second}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	preHandler := api.NewServer(api.Deps{Auth: service.NewAuthService(prePool, inputs.TargetJWT, time.Minute, time.Hour), Log: logger}).Handler()
	postHandler := api.NewServer(api.Deps{
		Auth:      service.NewAuthService(postPool, inputs.TargetJWT, time.Minute, time.Hour),
		Books:     service.NewBookService(postPool, provider, logger),
		Sources:   service.NewSourceService(postPool),
		TOC:       service.NewTOCService(postPool, ft, sb),
		Content:   service.NewContentService(postPool, ft, sb, nil, provider, logger),
		Progress:  service.NewProgressService(postPool),
		Bookmarks: service.NewBookmarkService(postPool, logger),
		Storage:   provider,
		Log:       logger,
	}).Handler()
	var activeHandler atomic.Pointer[http.Handler]
	activeHandler.Store(&preHandler)
	var loginRequests, logoutRequests, oldAccessRequests, oldRefreshRequests, readerRequests, trapRequests, unsafe atomic.Int32
	trap := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { trapRequests.Add(1); w.WriteHeader(500) }))
	defer trap.Close()
	t.Setenv("HTTP_PROXY", trap.URL)
	t.Setenv("HTTPS_PROXY", trap.URL)
	t.Setenv("NO_PROXY", "")
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") != "" {
			unsafe.Add(1)
		}
		if r.URL.Path == "/api/v1/auth/login" {
			loginRequests.Add(1)
			if r.Header.Get("Authorization") != "" {
				unsafe.Add(1)
			}
			if mode == "redirect" {
				http.Redirect(w, r, trap.URL, http.StatusTemporaryRedirect)
				return
			}
			if mode == "lost" {
				record := httptest.NewRecorder()
				(*activeHandler.Load()).ServeHTTP(record, r)
				if record.Code != 200 {
					unsafe.Add(1)
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					unsafe.Add(1)
					return
				}
				conn.Close()
				return
			}
		} else if r.URL.Path == "/api/v1/auth/logout" {
			logoutRequests.Add(1)
		} else if r.URL.Path == "/api/v1/auth/me" && mode == "good" {
			oldAccessRequests.Add(1)
			if r.Method != http.MethodGet || r.ContentLength != 0 {
				unsafe.Add(1)
			}
		} else if r.URL.Path == "/api/v1/auth/refresh" && mode == "good" {
			oldRefreshRequests.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
				unsafe.Add(1)
			}
		} else if mode == "good" && isAllowedReaderHTTPRequest(r) {
			readerRequests.Add(1)
		} else {
			unsafe.Add(1)
		}
		(*activeHandler.Load()).ServeHTTP(w, r)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	certificate := append([]byte(nil), server.Certificate().Raw...)
	if mode == "wrong_pin" {
		certificate[len(certificate)-1] ^= 1
	}
	transport, e := newBehaviorHTTPTransport(behaviorHTTPConfig{Endpoint: server.URL, Certificate: certificate, Binding: behaviorTransportBinding{p.TargetID, draft.TargetIdentity.Database, a.RuntimeSHA}, DatabaseURL: raw, Source: inputs.SourceDatabase, Target: inputs.TargetDatabase, JWT: inputs.TargetJWT, Users: users, Clock: clock})
	if e != nil {
		t.Fatal(e)
	}
	defer transport.Close()
	var result behaviorDispatchObservation
	t.Run("https_dispatch", func(t *testing.T) {
		var proof *verifiedBehaviorAccounts
		if mode == "good" {
			proof, e = g.verifyBehaviorAccounts(ctx, prepared, inputs, clock)
			if e != nil {
				t.Fatal("pre-login verified account proof failed", e)
			}
			for i, credential := range prepared.accounts {
				if transport.confirmOldAccess(ctx, proof, users[i], f.old[i].User.TokenVersion+1, credential.OldAccess, inputs.SourceJWT) != nil {
					t.Fatal("valid source access token rejection unconfirmed")
				}
				if transport.confirmOldAccess(ctx, proof, users[i], f.old[i].User.TokenVersion+1, credential.OldAccess, inputs.SourceJWT) != errBehaviorHTTP {
					t.Fatal("old access probe replayed")
				}
			}
			if transport.rejectOldRefresh(ctx, proof, users[0], prepared.accounts[0].OldRefresh) != nil {
				t.Fatal("pre-login old refresh token rejection unconfirmed")
			}
			if transport.rejectOldRefresh(ctx, proof, users[0], prepared.accounts[0].OldRefresh) != errBehaviorHTTP || oldRefreshRequests.Load() != 1 {
				t.Fatal("old refresh probe replayed before login")
			}
		}
		activeHandler.Store(&postHandler)
		prePool.Close()
		result, e = g.dispatchBehaviorOnce(ctx, prepared, inputs, transport, clock)
		wantHTTPStage := "login_transport"
		if mode == "good" {
			wantHTTPStage = "completed"
		}
		if result.Stage != wantHTTPStage {
			t.Fatalf("HTTPS dispatch stage %s, expected %s", result.Stage, wantHTTPStage)
		}
		if mode == "good" {
			if e != nil || !result.ResponsesObserved || len(result.Sessions) != 2 {
				t.Fatal("HTTPS dispatch failed", e)
			}
			beforeRows := snapshotAuthSessions(t, ctx, postPool)
			if transport.rejectOldRefresh(ctx, proof, users[1], prepared.accounts[1].OldRefresh) != nil {
				t.Fatal("post-logout old refresh token rejection unconfirmed")
			}
			if transport.rejectOldRefresh(ctx, proof, users[1], prepared.accounts[1].OldRefresh) != errBehaviorHTTP || transport.rejectOldRefresh(ctx, proof, users[0], prepared.accounts[0].OldRefresh) != errBehaviorHTTP || oldRefreshRequests.Load() != 2 {
				t.Fatal("old refresh probe replayed after logout")
			}
			if snapshotAuthSessions(t, ctx, postPool) != beforeRows {
				t.Fatal("auth_sessions mutated by old refresh probe")
			}
		} else if e != errBehaviorDispatch || result.ResponsesObserved {
			t.Fatal("unsafe HTTPS response accepted")
		}
	})
	t.Run("https_no_replay", func(t *testing.T) {
		before := loginRequests.Load()
		if _, e := g.dispatchBehaviorOnce(ctx, prepared, inputs, transport, clock); e != errBehaviorDispatch {
			t.Fatal("guard replay allowed")
		}
		credential := prepared.accounts[0]
		credential.OldAccess, credential.OldRefresh = "", ""
		if _, e := transport.Login(ctx, credential); e != errBehaviorHTTP || loginRequests.Load() != before {
			t.Fatal("adapter replayed POST")
		}
	})
	newSessions, active, wantLogin, wantLogout := 0, 0, 0, 0
	if mode == "good" {
		newSessions, wantLogin, wantLogout = 2, 2, 2
	}
	if mode == "lost" {
		newSessions, active, wantLogin = 1, 1, 1
	}
	if mode == "redirect" {
		wantLogin = 1
	}
	t.Run("https_session_bounds", func(t *testing.T) {
		var count, actualActive, foreign int
		if postPool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE revoked_at IS NULL),count(*) FILTER(WHERE user_id<>$1 AND user_id<>$2) FROM public.auth_sessions`, users[0], users[1]).Scan(&count, &actualActive, &foreign) != nil || count != 2+newSessions || actualActive != active || foreign != 0 {
			t.Fatal("unexpected HTTPS session delta")
		}
		for _, s := range result.Sessions {
			var revoked bool
			if postPool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM public.auth_sessions WHERE id=$1 AND user_id=$2`, s.SessionID, s.UserID).Scan(&revoked) != nil || !revoked {
				t.Fatal("known HTTPS session not revoked")
			}
		}
	})
	t.Run("https_network_safety", func(t *testing.T) {
		wantOld, wantReader := int32(0), int32(0)
		if mode == "good" {
			wantOld, wantReader = 2, 12
		}
		if oldAccessRequests.Load() != wantOld || oldRefreshRequests.Load() != wantOld || readerRequests.Load() != wantReader || ft.calls.Load() != 0 {
			t.Fatal("unexpected old token, reader, or external fetch request count")
		}
		if int(loginRequests.Load()) != wantLogin || int(logoutRequests.Load()) != wantLogout || trapRequests.Load() != 0 || unsafe.Load() != 0 {
			t.Fatal("redirect/proxy/retry or credential boundary violated")
		}
		d := DiagnoseBehaviorIntent(ctx, g.locations.files[3], p.OperationID, clock())
		if d.Status != "outcome_unknown" || d.Dispatch || d.Retry {
			t.Fatal("HTTP records granted retry")
		}
	})
	if os.Getenv("BACKUP_BEHAVIOR_REVIEW") == "1" {
		if mode == "good" {
			server.Close()
			postPool.Close()
			transport.Close()
		}
		assertBoundBehaviorReview(t, g, inputs, clock, mode)
	}
	// The dispatcher never searches for unknown sessions. Only this explicit
	// fixture operator cleans the intentionally lost response after assertions.
	if mode == "lost" {
		var id uuid.UUID
		if postPool.QueryRow(ctx, `SELECT id FROM public.auth_sessions WHERE user_id=$1 AND revoked_at IS NULL`, users[0]).Scan(&id) != nil {
			t.Fatal("lost fixture session")
		}
		if _, e := postPool.Exec(ctx, `UPDATE public.auth_sessions SET revoked_at=now() WHERE id=$1`, id); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf(`BEHAVIOR_HTTP_EVIDENCE {"scope":"isolated-https-dispatch","mode":%q,"login_requests":%d,"logout_requests":%d,"new_sessions":%d,"active_after_dispatch":%d,"old_access_requests":%d,"old_refresh_requests":%d,"reader_requests":%d,"external_fetches":%d,"redirect_or_proxy_requests":0,"deployment_authorized":false}`, mode, loginRequests.Load(), logoutRequests.Load(), newSessions, active, oldAccessRequests.Load(), oldRefreshRequests.Load(), readerRequests.Load(), ft.calls.Load())
}

func snapshotAuthSessions(t *testing.T, ctx context.Context, queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) string {
	t.Helper()
	rows, e := queryer.Query(ctx, `SELECT id::text,user_id::text,COALESCE(to_char(revoked_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'<null>') FROM public.auth_sessions ORDER BY id`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, uid, rev string
		if e := rows.Scan(&id, &uid, &rev); e != nil {
			t.Fatal(e)
		}
		lines = append(lines, id+"|"+uid+"|"+rev)
	}
	if e := rows.Err(); e != nil {
		t.Fatal(e)
	}
	return strings.Join(lines, "\n")
}
