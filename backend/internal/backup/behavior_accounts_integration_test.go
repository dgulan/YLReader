//go:build integration && linux

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	headercrypto "yreader/backend/internal/pkg/crypto"
)

func assertRestoredBehaviorAccounts(t *testing.T, g *restoreGuard, stage *stagedObjects, p Plan, owner, raw string, ring *headercrypto.Keyring, source *pgx.Conn, f *httpFixtureCredentials) {
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
	prepared, e := prepareBehaviorInput(ctx, &diagnosticReader{data: data}, g.locations.files[3], "consent.json", p, draft.TargetIdentity.Runtime, clock)
	if e != nil {
		t.Fatal(e)
	}
	defer prepared.Close()
	admin := func(query string, args ...any) {
		t.Helper()
		cfg, e := ParseTarget(raw)
		if e != nil {
			t.Fatal(e)
		}
		cfg.User = "postgres"
		cfg.Password = ""
		conn, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close(ctx)
		if _, e = conn.Exec(ctx, query, args...); e != nil {
			t.Fatal("owned account fixture mutation failed")
		}
	}
	snapshot := func() string {
		t.Helper()
		cfg, e := ParseTarget(raw)
		if e != nil {
			t.Fatal(e)
		}
		cfg.User = "postgres"
		cfg.Password = ""
		conn, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close(ctx)
		var value string
		if conn.QueryRow(ctx, `SELECT json_build_array((SELECT json_agg(t ORDER BY id) FROM public.users t),(SELECT json_agg(t ORDER BY id) FROM public.auth_sessions t),(SELECT json_agg(t ORDER BY version) FROM public.schema_migrations t))::text`).Scan(&value) != nil {
			t.Fatal("account fixture snapshot")
		}
		return value
	}
	beforeControl := statusFixtureBytes(t, g.config.Control.Root+"/"+g.config.Control.Namespace)
	names := []string{"valid", "wrong_password", "wrong_username", "disabled", "expired_access", "wrong_access_key", "wrong_access_version", "foreign_refresh", "unrevoked_refresh", "same_jwt", "wrong_pin", "wrong_runtime", "wrong_owner", "closed_guard"}
	for _, name := range names {
		t.Run("restored_accounts_"+name, func(t *testing.T) {
			candidate := &preparedBehaviorInput{consent: prepared.consent, accounts: append([]behaviorCredential(nil), prepared.accounts...)}
			defer candidate.Close()
			i := inputs
			guard := g
			sign := func(exp time.Time, version int, key []byte) string {
				claims := behaviorClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: candidate.accounts[0].UserID, IssuedAt: jwt.NewNumericDate(clock().Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(exp)}, TokenVersion: version}
				token, e := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
				if e != nil {
					t.Fatal(e)
				}
				return token
			}
			switch name {
			case "wrong_password":
				candidate.accounts[0].Password = "wrong"
			case "wrong_username":
				candidate.accounts[0].Username = "not-the-restored-account"
			case "disabled":
				admin(`UPDATE public.users SET is_disabled=true WHERE id=$1`, f.old[0].User.ID)
				defer admin(`UPDATE public.users SET is_disabled=false WHERE id=$1`, f.old[0].User.ID)
			case "expired_access":
				candidate.accounts[0].OldAccess = sign(clock().Add(-time.Second), f.old[0].User.TokenVersion, f.secret)
			case "wrong_access_key":
				candidate.accounts[0].OldAccess = sign(clock().Add(time.Minute), f.old[0].User.TokenVersion, i.TargetJWT)
			case "wrong_access_version":
				candidate.accounts[0].OldAccess = sign(clock().Add(time.Minute), f.old[0].User.TokenVersion+1, f.secret)
			case "foreign_refresh":
				candidate.accounts[0].OldRefresh = candidate.accounts[1].OldRefresh
			case "unrevoked_refresh":
				// Preserve the exact revocation stamp after the deliberate fixture change.
				cfg, _ := ParseTarget(raw)
				cfg.User = "postgres"
				conn, e := pgx.ConnectConfig(ctx, cfg)
				if e != nil {
					t.Fatal(e)
				}
				var revoked time.Time
				if conn.QueryRow(ctx, `SELECT revoked_at FROM public.auth_sessions WHERE user_id=$1 LIMIT 1`, f.old[0].User.ID).Scan(&revoked) != nil {
					conn.Close(ctx)
					t.Fatal("revocation stamp")
				}
				conn.Close(ctx)
				admin(`UPDATE public.auth_sessions SET revoked_at=NULL WHERE user_id=$1`, f.old[0].User.ID)
				defer admin(`UPDATE public.auth_sessions SET revoked_at=$1 WHERE user_id=$2`, revoked, f.old[0].User.ID)
			case "same_jwt":
				i.TargetJWT = i.SourceJWT
			case "wrong_pin":
				i.TargetDatabase.DatabaseOID++
			case "wrong_runtime":
				i.ActualRuntime.ConfigRevision = uuid.NewString()
			case "wrong_owner":
				i.OwnerID = uuid.NewString()
			case "closed_guard":
				guard = &restoreGuard{plan: p}
				guard.Close()
				i.Guard = guard
			}
			beforeProbe := snapshot()
			proof, e := guard.verifyBehaviorAccounts(ctx, candidate, i, clock)
			if snapshot() != beforeProbe {
				t.Fatal("account checker changed database rows")
			}
			if name == "valid" {
				if e != nil || proof == nil || proof.guard != g || proof.plan != p.Digest || proof.consent != a.ID || proof.records != s.tokens || len(proof.users) != 2 || proof.users[0] != f.old[0].User.ID || proof.runtime != a.RuntimeSHA {
					t.Fatalf("guarded account observation failed: err=%v stage=%s", e, guard.accountStageValue())
				}
				if _, e = json.Marshal(proof); e == nil {
					t.Fatal("account observation serialized")
				}
				if strings.Contains(fmt.Sprintf("%#v", *proof), f.password) {
					t.Fatal("credential leaked")
				}
			} else if e != errBehaviorAccounts || proof != nil {
				t.Fatal("unsafe account input accepted")
			}
		})
	}
	afterControl := statusFixtureBytes(t, g.config.Control.Root+"/"+g.config.Control.Namespace)
	if len(beforeControl) != len(afterControl) {
		t.Fatal("account checker wrote control records")
	}
	for key, value := range beforeControl {
		if afterControl[key] != value {
			t.Fatal("account checker changed control records")
		}
	}
	t.Log(`RESTORED_ACCOUNTS_EVIDENCE {"scope":"guarded-synthetic-accounts","checks":14,"database_and_control_unchanged":true,"login_dispatched":false,"execution_authorized":false}`)
}
