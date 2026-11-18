//go:build integration && linux

package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/service"
)

// Test adapter only: direct AuthService against the owned isolated target.
// It is deliberately not an externally configurable production transport.
type serviceDispatchFixture struct {
	pool            *pgxpool.Pool
	auth            *service.AuthService
	binding         behaviorTransportBinding
	refresh         map[uuid.UUID]string
	mode            string
	logins, logouts int
}

func (f *serviceDispatchFixture) Binding() behaviorTransportBinding { return f.binding }
func (f *serviceDispatchFixture) Login(ctx context.Context, c behaviorCredential) (behaviorSessionResponse, error) {
	f.logins++
	pair, e := f.auth.Login(ctx, c.Username, c.Password)
	if e != nil {
		return behaviorSessionResponse{}, e
	}
	sum := sha256.Sum256([]byte(pair.RefreshToken))
	var response behaviorSessionResponse
	if f.pool.QueryRow(ctx, `SELECT user_id,id FROM public.auth_sessions WHERE refresh_token_hash=$1 AND revoked_at IS NULL`, hex.EncodeToString(sum[:])).Scan(&response.UserID, &response.SessionID) != nil || response.UserID.String() != c.UserID {
		return behaviorSessionResponse{}, errBehaviorDispatch
	}
	f.refresh[response.SessionID] = pair.RefreshToken
	if f.mode == "lost" {
		return behaviorSessionResponse{}, errors.New("synthetic response loss after commit")
	}
	return response, nil
}
func (f *serviceDispatchFixture) Logout(ctx context.Context, s behaviorSessionResponse) error {
	f.logouts++
	token, ok := f.refresh[s.SessionID]
	if !ok {
		return errBehaviorDispatch
	}
	if e := f.auth.Logout(ctx, token); e != nil {
		return e
	}
	var revoked bool
	if f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM public.auth_sessions WHERE id=$1 AND user_id=$2`, s.SessionID, s.UserID).Scan(&revoked) != nil || !revoked {
		return errBehaviorDispatch
	}
	delete(f.refresh, s.SessionID)
	return nil
}

type dispatchAckBackup struct {
	storage.BackupFiles
	mode string
}

func (f dispatchAckBackup) CreateOnly(ctx context.Context, key string, r io.Reader, n int64) error {
	return (dispatchFaultFiles{ConditionalFiles: f.BackupFiles, mode: f.mode}).CreateOnly(ctx, key, r, n)
}

func assertBehaviorDispatch(t *testing.T, g *restoreGuard, stage *stagedObjects, p Plan, owner, raw, mode string, ring *headercrypto.Keyring, source *pgx.Conn, f *httpFixtureCredentials) {
	t.Helper()
	ctx := context.Background()
	clock := func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
	draft, _, release, inputs := fixtureBehaviorDraft(t, g, stage, p, owner, raw, ring, source, f.secret, fixtureSecret(t))
	defer release()
	s, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	a := behaviorConsent{Format: "ylreader-behavior-consent", Version: 1, Scope: "isolated-account-verification-only", ID: uuid.NewString(), OperationID: p.OperationID, OwnerID: owner, PlanDigest: p.Digest, TargetID: p.TargetID, Generation: s.op.Generation, OperationSHA: s.tokens[0], AttemptSHA: s.tokens[1], PublicationSHA: s.tokens[2], RuntimeSHA: draft.TargetIdentity.Runtime, Issued: clock(), Expires: clock().Add(time.Minute)}
	data := map[string][]byte{}
	for i, name := range f.names {
		key := fmt.Sprintf("account-%d.json", i)
		id := f.old[i].User.ID.String()
		a.Accounts = append(a.Accounts, behaviorConsentAccount{id, key})
		data[key], _ = json.Marshal(map[string]string{"user_id": id, "username": name, "password": f.password, "old_access_token": f.old[i].AccessToken, "old_refresh_token": f.old[i].RefreshToken})
	}
	data["consent.json"], _ = json.Marshal(a)
	prepared, e := prepareBehaviorInput(ctx, &diagnosticReader{data: data}, g.locations.files[3], "consent.json", p, a.RuntimeSHA, clock)
	if e != nil {
		t.Fatal(e)
	}
	defer prepared.Close()
	pool := newIsolatedTargetPool(t, ctx, raw)
	defer pool.Close()
	transport := &serviceDispatchFixture{pool: pool, auth: service.NewAuthService(pool, inputs.TargetJWT, time.Minute, time.Hour), binding: behaviorTransportBinding{p.TargetID, draft.TargetIdentity.Database, draft.TargetIdentity.Runtime}, refresh: map[uuid.UUID]string{}, mode: mode}
	if mode == "intent_ack" || mode == "response_ack" {
		g.locations.files[3] = dispatchAckBackup{g.locations.files[3], mode}
	}
	wantStage := "completed"
	switch mode {
	case "lost":
		wantStage = "login_transport"
	case "intent_ack":
		wantStage = "intent_record"
	case "response_ack":
		wantStage = "post_login_persist_or_logout"
	}
	var observation behaviorDispatchObservation
	t.Run("dispatch_run", func(t *testing.T) {
		observation, e = g.dispatchBehaviorOnce(ctx, prepared, inputs, transport, clock)
		if observation.Stage != wantStage {
			t.Fatalf("dispatch stage %s, expected %s", observation.Stage, wantStage)
		}
		if mode == "good" {
			if e != nil || !observation.ResponsesObserved || len(observation.Sessions) != 2 {
				t.Fatal("bounded service dispatch failed", e)
			}
		} else if e != errBehaviorDispatch || observation.ResponsesObserved {
			t.Fatal("uncertain dispatch reported success")
		}
	})
	t.Run("dispatch_replay", func(t *testing.T) {
		calls := transport.logins
		if r, e := g.dispatchBehaviorOnce(ctx, prepared, inputs, transport, clock); e != errBehaviorDispatch || r.Stage != "guard_replay_or_closed" || transport.logins != calls {
			t.Fatal("same guard replayed login")
		}
	})
	wantLogin, wantLogout, wantActive := 2, 2, 0
	if mode == "lost" {
		wantLogin, wantLogout, wantActive = 1, 0, 1
	}
	if mode == "intent_ack" {
		wantLogin, wantLogout = 0, 0
	}
	if mode == "response_ack" {
		wantLogin, wantLogout = 1, 1
	}
	t.Run("dispatch_session_bounds", func(t *testing.T) {
		var count, active, foreign int
		e := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE revoked_at IS NULL),count(*) FILTER(WHERE user_id<>$1 AND user_id<>$2) FROM public.auth_sessions`, f.old[0].User.ID, f.old[1].User.ID).Scan(&count, &active, &foreign)
		if e != nil || transport.logins != wantLogin || transport.logouts != wantLogout || count != 2+wantLogin || active != wantActive || foreign != 0 {
			t.Fatal("unexpected session delta or call count")
		}
	})
	t.Run("dispatch_diagnostic", func(t *testing.T) {
		d := DiagnoseBehaviorIntent(ctx, g.locations.files[3], p.OperationID, clock())
		if d.Status != "outcome_unknown" || d.Persistence != "not_inferred" || d.Dispatch || d.Retry {
			t.Fatalf("disk inferred retry: status=%s reason=%s stage=%s", d.Status, d.Reason, observation.Stage)
		}
		for i := 0; i < 2; i++ {
			for _, file := range []string{"dispatch.json", "response.json", "logout.json"} {
				key := fmt.Sprintf("operations/%s/behavior-actions/%d/%s", p.OperationID, i, file)
				b, present, err := optionalStatusBytes(ctx, g.locations.files[3], key, 8192)
				if err != nil {
					t.Fatal(err)
				}
				if present && (strings.Contains(string(b), f.password) || strings.Contains(string(b), "refresh_token")) {
					t.Fatal("secret in ledger")
				}
			}
		}
	})
	if os.Getenv("BACKUP_BEHAVIOR_LEDGER") == "1" {
		assertBehaviorLedgerReview(t, g, p, raw, mode, transport, inputs, clock)
	}
	// Only the test operator cleans the intentionally lost response afterwards;
	// the dispatcher itself never searches for or revokes an unknown session.
	for id, token := range transport.refresh {
		if e := transport.auth.Logout(ctx, token); e != nil {
			t.Fatal(e)
		}
		delete(transport.refresh, id)
	}
	t.Logf(`BEHAVIOR_DISPATCH_EVIDENCE {"scope":"isolated-service-dispatch","mode":%q,"login_calls":%d,"logout_calls":%d,"unknown_response_retained":%t,"deployment_authorized":false}`, mode, wantLogin, wantLogout, mode != "good")
}
