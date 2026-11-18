package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/storage"
)

type dispatchFakeTransport struct {
	binding         behaviorTransportBinding
	mu              sync.Mutex
	logins, logouts int
	mode            string
	cancel          context.CancelFunc
}

func (f *dispatchFakeTransport) Binding() behaviorTransportBinding { return f.binding }
func (f *dispatchFakeTransport) Login(ctx context.Context, c behaviorCredential) (behaviorSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins++
	if c.OldAccess != "" || c.OldRefresh != "" {
		return behaviorSessionResponse{}, errors.New("old credentials must not reach transport")
	}
	if f.mode == "login_unknown" {
		return behaviorSessionResponse{}, errors.New("SECRET lost response")
	}
	id := uuid.MustParse(c.UserID)
	if f.mode == "wrong_user" {
		id = uuid.New()
	}
	sid := uuid.New()
	if f.mode == "duplicate_session" {
		sid = uuid.MustParse("70000000-0000-4000-8000-000000000001")
	}
	if f.mode == "nil_session" {
		sid = uuid.Nil
	}
	if f.mode == "cancel_known" {
		f.cancel()
	}
	return behaviorSessionResponse{id, sid}, nil
}
func (f *dispatchFakeTransport) Logout(ctx context.Context, s behaviorSessionResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logouts++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.mode == "logout_unknown" {
		return errors.New("SECRET logout response")
	}
	return nil
}
func dispatchFixture(t *testing.T) (*preparedBehaviorInput, *memoryFiles, *restoreGuard, *verifiedBehaviorAccounts, *dispatchFakeTransport, Plan, func() time.Time) {
	t.Helper()
	prepared, files, p, clock := intentFixture(t)
	g := &restoreGuard{plan: p}
	proof := &verifiedBehaviorAccounts{guard: g, plan: p.Digest, consent: prepared.consent.ID, runtime: prepared.consent.RuntimeSHA, database: strings.Repeat("d", 64), records: [3]string{prepared.consent.OperationSHA, prepared.consent.AttemptSHA, prepared.consent.PublicationSHA}, users: []uuid.UUID{uuid.MustParse(prepared.accounts[0].UserID)}, checked: clock(), expires: prepared.consent.Expires}
	transport := &dispatchFakeTransport{binding: behaviorTransportBinding{p.TargetID, proof.database, proof.runtime}}
	return prepared, files, g, proof, transport, p, clock
}
func TestBehaviorDispatchOnceAndPrivateLedger(t *testing.T) {
	prepared, files, g, proof, transport, p, clock := dispatchFixture(t)
	r, e := dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil })
	if e != nil || !r.ResponsesObserved || len(r.Sessions) != 1 || transport.logins != 1 || transport.logouts != 1 {
		t.Fatal("one bounded login/logout required", e)
	}
	for key, data := range files.data {
		if strings.Contains(key, "behavior-actions/") {
			var a behaviorAction
			if exactJSON(data, &a) != nil || a.validate() != nil || strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "account.json") {
				t.Fatal("invalid/private ledger")
			}
		}
	}
	if _, e = json.Marshal(r); e == nil {
		t.Fatal("response observation exported")
	}
	if _, e = dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil }); e != errBehaviorDispatch || transport.logins != 1 {
		t.Fatal("persistent replay allowed")
	}
}

type dispatchFaultFiles struct {
	storage.ConditionalFiles
	mode string
}

func (f dispatchFaultFiles) CreateOnly(ctx context.Context, key string, r io.Reader, n int64) error {
	if f.mode == "response_fail" && strings.HasSuffix(key, "/response.json") {
		return errors.New("SECRET disk")
	}
	if e := f.ConditionalFiles.CreateOnly(ctx, key, r, n); e != nil {
		return e
	}
	if (f.mode == "intent_ack" && strings.HasSuffix(key, "/behavior-intent.json")) || (f.mode == "dispatch_ack" && strings.HasSuffix(key, "/dispatch.json")) || (f.mode == "response_ack" && strings.HasSuffix(key, "/response.json")) || (f.mode == "logout_ack" && strings.HasSuffix(key, "/logout.json")) {
		return storage.ErrCommitUnknown
	}
	return nil
}
func TestBehaviorDispatchFaultsNeverRepeatLogin(t *testing.T) {
	for _, mode := range []string{"intent_ack", "dispatch_ack", "response_ack", "response_fail", "logout_ack", "login_unknown", "logout_unknown", "wrong_user", "nil_session", "cancel_known"} {
		t.Run(mode, func(t *testing.T) {
			prepared, files, g, proof, transport, p, clock := dispatchFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport.mode = mode
			transport.cancel = cancel
			obs, e := dispatchBehaviorCore(ctx, dispatchFaultFiles{files, mode}, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil })
			if e != errBehaviorDispatch || strings.Contains(e.Error(), "SECRET") {
				t.Fatal("uncertain result allowed")
			}
			wantStage := "post_login_persist_or_logout"
			switch mode {
			case "intent_ack":
				wantStage = "intent_record"
			case "dispatch_ack":
				wantStage = "dispatch_action_persist"
			case "login_unknown", "wrong_user", "nil_session":
				wantStage = "login_transport"
			}
			if obs.Stage != wantStage {
				t.Fatalf("stage %s, expected %s", obs.Stage, wantStage)
			}
			wantLogin, wantLogout := 1, 1
			if mode == "intent_ack" || mode == "dispatch_ack" {
				wantLogin, wantLogout = 0, 0
			}
			if mode == "login_unknown" || mode == "wrong_user" || mode == "nil_session" {
				wantLogout = 0
			}
			if transport.logins != wantLogin || transport.logouts != wantLogout {
				t.Fatal("wrong bounded call counts")
			}
			if _, e = dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil }); e != errBehaviorDispatch || transport.logins != wantLogin {
				t.Fatal("lost acknowledgement permitted retry")
			}
		})
	}
}

func TestBehaviorDispatchBindingAndFreshness(t *testing.T) {
	for _, mode := range []string{"binding", "guard", "records", "stale", "expired", "recheck", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			prepared, files, g, proof, transport, p, clock := dispatchFixture(t)
			when := clock
			check := func(context.Context) error { return nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "binding":
				transport.binding.TargetID = prepared.consent.ID
			case "guard":
				proof.guard = &restoreGuard{}
			case "records":
				proof.records[0] = ""
			case "stale":
				proof.checked = clock().Add(-21 * time.Second)
			case "expired":
				when = func() time.Time { return prepared.consent.Expires }
			case "recheck":
				check = func(context.Context) error { return errors.New("SECRET") }
			case "cancelled":
				cancel()
			}
			before := len(files.data)
			if _, e := dispatchBehaviorCore(ctx, files, g, p, prepared, proof, transport, when, check); e != errBehaviorDispatch || transport.logins != 0 || len(files.data) != before {
				t.Fatal("invalid observation dispatched")
			}
		})
	}
}

func TestBehaviorDispatchConcurrentDurableWinner(t *testing.T) {
	prepared, files, g, proof, transport, p, clock := dispatchFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil })
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		} else if e != errBehaviorDispatch {
			t.Fatal(e)
		}
	}
	if wins != 1 || transport.logins != 1 || transport.logouts != 1 {
		t.Fatal("more than one durable batch dispatched")
	}
}

func TestBehaviorDispatchRejectsDuplicateSession(t *testing.T) {
	prepared, files, g, proof, transport, p, clock := dispatchFixture(t)
	second := uuid.New()
	prepared.consent.Accounts = append(append([]behaviorConsentAccount(nil), prepared.consent.Accounts...), behaviorConsentAccount{second.String(), "second.json"})
	credential := prepared.accounts[0]
	credential.UserID = second.String()
	prepared.accounts = append(prepared.accounts, credential)
	proof.users = append(proof.users, second)
	transport.mode = "duplicate_session"
	result, e := dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil })
	if e != errBehaviorDispatch || result.ResponsesObserved || transport.logins != 2 || transport.logouts != 1 {
		t.Fatal("duplicate session granted logout or next action")
	}
}
