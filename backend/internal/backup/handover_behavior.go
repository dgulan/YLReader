package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/service"
)

var errHandoverBehavior = errors.New("backup: authorized isolated behavior unconfirmed")

type behaviorAccount struct {
	ID                                        uuid.UUID
	Username, Password, OldAccess, OldRefresh string
}
type behaviorAuthorization struct {
	profile, id string
	accounts    []behaviorAccount
}
type behaviorGrant struct {
	profile, id string
	users       []uuid.UUID
}
type behaviorClaims struct {
	jwt.RegisteredClaims
	TokenVersion int `json:"tv"`
}

func checkOldBehaviorAccess(raw string, key []byte, id uuid.UUID, currentVersion int) error {
	return checkOldBehaviorAccessAt(raw, key, id, currentVersion, time.Now())
}

func checkOldBehaviorAccessAt(raw string, key []byte, id uuid.UUID, currentVersion int, now time.Time) error {
	if len(raw) == 0 || len(raw) > 16384 || len(key) < 32 || len(key) > 4096 || id == uuid.Nil || currentVersion <= 0 {
		return errHandoverBehavior
	}
	var claims behaviorClaims
	parsed, e := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errHandoverBehavior
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return now }))
	if e != nil || parsed == nil || !parsed.Valid || claims.IssuedAt == nil || claims.Subject != id.String() || claims.TokenVersion < 0 || claims.TokenVersion != currentVersion-1 {
		return errHandoverBehavior
	}
	return nil
}

// Internal isolated-fixture authorization only. A real-user credential CLI and
// generic production HTTP runner are deliberately not provided by this bridge.
func validateBehaviorAccounts(ctx context.Context, pool *pgxpool.Pool, sourceKey, targetKey []byte, a behaviorAuthorization) (behaviorGrant, error) {
	if ctx == nil || pool == nil || a.profile != "isolated-http-fixture-v1" || !validUUID(a.id) || len(a.accounts) < 1 || len(a.accounts) > 8 ||
		len(targetKey) < 32 || len(targetKey) > 4096 || bytes.Equal(sourceKey, targetKey) {
		return behaviorGrant{}, errHandoverBehavior
	}
	g := behaviorGrant{profile: a.profile, id: a.id}
	seen := map[uuid.UUID]bool{}
	for _, account := range a.accounts {
		if account.ID == uuid.Nil || seen[account.ID] || len(account.Username) == 0 || len(account.Username) > 256 || len(account.Password) == 0 || len(account.Password) > 4096 || len(account.OldRefresh) != 64 {
			return behaviorGrant{}, errHandoverBehavior
		}
		seen[account.ID] = true
		var username, hash string
		var version int
		var disabled bool
		if pool.QueryRow(ctx, `SELECT username,password_hash,token_version,is_disabled FROM public.users WHERE id=$1`, account.ID).Scan(&username, &hash, &version, &disabled) != nil || disabled || username != account.Username || checkOldBehaviorAccess(account.OldAccess, sourceKey, account.ID, version) != nil {
			return behaviorGrant{}, errHandoverBehavior
		}
		ok, e := service.VerifyPassword(hash, account.Password)
		if e != nil || !ok {
			return behaviorGrant{}, errHandoverBehavior
		}
		digest := sha256.Sum256([]byte(account.OldRefresh))
		var owner uuid.UUID
		var revoked bool
		var expires time.Time
		if pool.QueryRow(ctx, `SELECT user_id,revoked_at IS NOT NULL,expires_at FROM public.auth_sessions WHERE refresh_token_hash=$1`, hex.EncodeToString(digest[:])).Scan(&owner, &revoked, &expires) != nil || owner != account.ID || !revoked || !expires.After(time.Now()) {
			return behaviorGrant{}, errHandoverBehavior
		}
		g.users = append(g.users, account.ID)
	}
	return g, nil
}

type handoverSessionAllowance struct {
	ids, users []uuid.UUID
	start, end time.Time
}

func (a handoverSessionAllowance) valid() bool {
	if len(a.ids) < 1 || len(a.ids) > 32 || len(a.users) < 1 || len(a.users) > 8 || !validStamp(a.start) || !validStamp(a.end) || a.end.Before(a.start) || a.end.Sub(a.start) > 5*time.Minute {
		return false
	}
	for _, list := range [][]uuid.UUID{a.ids, a.users} {
		seen := map[uuid.UUID]bool{}
		for _, id := range list {
			if id == uuid.Nil || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return true
}

type handoverBehaviorProof struct {
	data     handoverDataProof
	grant    behaviorGrant
	sessions handoverSessionAllowance
	checks   [10]bool
}

func checkHandoverBehaviorProof(p Plan, s diagnosticSnapshot, base handoverReview, proof handoverBehaviorProof, now time.Time) error {
	if base.validateBindingsFor(p, s, base.TargetIdentity, now) != nil || !proof.data.matches(p, s) || proof.grant.profile != "isolated-http-fixture-v1" || !validUUID(proof.grant.id) || !proof.sessions.valid() ||
		proof.sessions.start.Before(base.Finished) || proof.sessions.end.After(now) || !proof.sessions.end.Before(base.Expires) || len(proof.grant.users) != len(proof.sessions.users) {
		return errHandoverBehavior
	}
	for i, id := range proof.grant.users {
		if id != proof.sessions.users[i] {
			return errHandoverBehavior
		}
	}
	for _, ok := range proof.checks {
		if !ok {
			return errHandoverBehavior
		}
	}
	for _, c := range base.Checks {
		expected := "pass"
		if c.ID == "jwt_rotation" || c.ID == "runtime_behavior" {
			expected = "unavailable"
		}
		if c.Status != expected {
			return errHandoverBehavior
		}
	}
	return nil
}

// Completes a draft for this isolated profile only. Login/session side effects
// remain explicit; this function only rechecks, never deletes or revokes rows.
func (g *restoreGuard) finalizeHandoverBehavior(ctx context.Context, stage *stagedObjects, owner string, ring *headercrypto.Keyring, kids []string, base handoverReview, proof handoverBehaviorProof, clock func() time.Time) (handoverReview, error) {
	if g == nil || ctx == nil || clock == nil {
		return handoverReview{}, errHandoverBehavior
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	g.mu.Lock()
	if g.locations == nil || g.finalReviewProof != nil {
		g.mu.Unlock()
		return handoverReview{}, errHandoverBehavior
	}
	files := g.locations.files[3]
	p := g.plan
	g.mu.Unlock()
	s, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || checkHandoverBehaviorProof(p, s, base, proof, clock()) != nil {
		return handoverReview{}, errHandoverBehavior
	}
	checked, e := g.probeHandoverDataSessions(ctx, stage, owner, p.TargetID, time.Now().UTC(), ring, kids, s, &proof.sessions)
	if e != nil || !checked.matches(p, s) {
		return handoverReview{}, errHandoverBehavior
	}
	base.Finished = clock()
	base.Checks = append([]handoverReviewCheck(nil), base.Checks...)
	for i, c := range base.Checks {
		if c.ID == "jwt_rotation" || c.ID == "runtime_behavior" {
			base.Checks[i].Status = "pass"
		}
	}
	after, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || after.tokens != s.tokens || base.validateFor(p, after, base.TargetIdentity, base.Finished) != nil {
		return handoverReview{}, errHandoverBehavior
	}
	encoded, _ := json.Marshal(base)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil || g.finalReviewProof != nil {
		return handoverReview{}, errHandoverBehavior
	}
	sessions := proof.sessions
	sessions.ids = append([]uuid.UUID(nil), sessions.ids...)
	sessions.users = append([]uuid.UUID(nil), sessions.users...)
	g.finalReviewProof = &finalizedReviewProof{sha: digestBytes(encoded), sessions: sessions}
	return base, nil
}
