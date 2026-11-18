package backup

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestBehaviorOldAccessPreflight(t *testing.T) {
	key := bytes.Repeat([]byte("a"), 32)
	id := uuid.New()
	now := time.Now()
	for _, name := range []string{"valid", "expired", "missing_exp", "missing_iat", "future_iat", "subject", "version", "algorithm", "key"} {
		t.Run(name, func(t *testing.T) {
			claims := behaviorClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: id.String(), IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, TokenVersion: 3}
			var method jwt.SigningMethod = jwt.SigningMethodHS256
			signKey := key
			switch name {
			case "expired":
				claims.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Second))
			case "missing_exp":
				claims.ExpiresAt = nil
			case "missing_iat":
				claims.IssuedAt = nil
			case "future_iat":
				claims.IssuedAt = jwt.NewNumericDate(now.Add(time.Hour))
			case "subject":
				claims.Subject = uuid.NewString()
			case "version":
				claims.TokenVersion = 4
			case "algorithm":
				method = jwt.SigningMethodHS384
			case "key":
				signKey = bytes.Repeat([]byte("b"), 32)
			}
			raw, e := jwt.NewWithClaims(method, claims).SignedString(signKey)
			if e != nil {
				t.Fatal(e)
			}
			e = checkOldBehaviorAccess(raw, key, id, 4)
			if (e == nil) != (name == "valid") {
				t.Fatal("old access preflight incorrect")
			}
			if e != nil && e != errHandoverBehavior {
				t.Fatal("token error leaked")
			}
		})
	}
}

func TestBehaviorSessionAllowanceBounds(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, name := range []string{"valid", "missing", "duplicate", "too_many", "wrong_user", "backwards", "long_window"} {
		t.Run(name, func(t *testing.T) {
			a := handoverSessionAllowance{ids: []uuid.UUID{uuid.New()}, users: []uuid.UUID{uuid.New()}, start: now, end: now.Add(time.Second)}
			switch name {
			case "missing":
				a.ids = nil
			case "duplicate":
				a.ids = append(a.ids, a.ids[0])
			case "too_many":
				for len(a.ids) < 33 {
					a.ids = append(a.ids, uuid.New())
				}
			case "wrong_user":
				a.users[0] = uuid.Nil
			case "backwards":
				a.end = now.Add(-time.Second)
			case "long_window":
				a.end = now.Add(6 * time.Minute)
			}
			if a.valid() != (name == "valid") {
				t.Fatal("unsafe session delta accepted")
			}
		})
	}
}

func TestBehaviorProofSameOperationAndWindow(t *testing.T) {
	for _, name := range []string{"valid", "profile", "authorization", "plan", "records", "tree", "unchecked", "different_user", "early", "expired", "partial_draft"} {
		t.Run(name, func(t *testing.T) {
			f, p, probes, clock := collectionFixture(t)
			probes.Data = func(_ context.Context, s diagnosticSnapshot) (handoverDataProof, error) {
				return handoverDataProof{p.Digest, s.tokens, s.pub.TreeFingerprint}, nil
			}
			base, e := collectHandoverReview(context.Background(), f, p, probes, clock)
			if e != nil {
				t.Fatal(e)
			}
			s, _ := stableHandoverSnapshot(context.Background(), f, p.OperationID)
			user := uuid.New()
			proof := handoverBehaviorProof{data: handoverDataProof{p.Digest, s.tokens, s.pub.TreeFingerprint}, grant: behaviorGrant{"isolated-http-fixture-v1", uuid.NewString(), []uuid.UUID{user}}, sessions: handoverSessionAllowance{[]uuid.UUID{uuid.New()}, []uuid.UUID{user}, base.Finished, base.Finished.Add(time.Second)}}
			for i := range proof.checks {
				proof.checks[i] = true
			}
			now := base.Finished.Add(2 * time.Second)
			switch name {
			case "profile":
				proof.grant.profile = "production"
			case "authorization":
				proof.grant.id = "secret"
			case "plan":
				proof.data.plan = ""
			case "records":
				proof.data.records[0] = ""
			case "tree":
				proof.data.tree = ""
			case "unchecked":
				proof.checks[0] = false
			case "different_user":
				proof.sessions.users[0] = uuid.New()
			case "early":
				proof.sessions.start = base.Finished.Add(-time.Microsecond)
			case "expired":
				now = base.Expires
			case "partial_draft":
				base.Checks[0].Status = "unavailable"
			}
			e = checkHandoverBehaviorProof(p, s, base, proof, now)
			if (e == nil) != (name == "valid") {
				t.Fatal("behavior binding/window incorrect")
			}
		})
	}
}
