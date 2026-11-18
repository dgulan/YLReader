package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/storage"
)

var errBehaviorReview = errors.New("backup: bound HTTPS account review unavailable or unconfirmed")

type confirmedOldTokenSet struct {
	access  map[uuid.UUID]oldTokenProbeWitness
	refresh map[uuid.UUID]oldTokenProbeWitness
	drifted bool
}

type confirmedReaderSet struct {
	authorized bool
	users      map[uuid.UUID]readerRoundtripWitness
	drifted    bool
}

type confirmedBehaviorRun struct {
	plan, consent, ledger, certificate string
	identity                           handoverTargetIdentity
	records                            [3]string
	sessions                           []behaviorSessionResponse
	windowStart                        time.Time
	started, finished, expires         time.Time
	reviewStarted, writeAttempted      bool
	reviewSHA                          string
	sourceJWT, targetJWT               []byte
	oldTokens                          confirmedOldTokenSet
	reader                             confirmedReaderSet
}

func (*confirmedBehaviorRun) MarshalJSON() ([]byte, error) { return nil, errBehaviorReview }
func (confirmedBehaviorRun) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[private confirmed behavior witness]")
}

func behaviorLedgerDigest(s behaviorLedgerSnapshot) string {
	var hashes [8][3]string
	for i := range hashes {
		for j := range hashes[i] {
			if s.present[i][j] {
				hashes[i][j] = digestBytes(s.actions[i][j])
			}
		}
	}
	data, _ := json.Marshal(struct {
		Control [3]string    `json:"control"`
		Intent  string       `json:"intent"`
		Actions [8][3]string `json:"actions"`
	}{s.base.control.tokens, digestBytes(s.base.raw), hashes})
	return digestBytes(data)
}

func captureConfirmedBehaviorRun(ctx context.Context, files RestoreRecordReader, p Plan, consent behaviorConsent, proof *verifiedBehaviorAccounts, transport *behaviorHTTPTransport, result behaviorDispatchObservation, sourceJWT, targetJWT []byte, now time.Time) (*confirmedBehaviorRun, error) {
	if ctx == nil || proof == nil || transport == nil || consent.validate() != nil || consent.ID != proof.consent || consent.PlanDigest != p.Digest || consent.TargetID != p.TargetID || consent.RuntimeSHA != proof.runtime || [3]string{consent.OperationSHA, consent.AttemptSHA, consent.PublicationSHA} != proof.records || !result.ResponsesObserved || len(result.Sessions) < 1 || len(result.Sessions) != len(proof.users) || !validSHA(proof.redis) {
		return nil, errBehaviorReview
	}
	certificate, e := transport.completedWitness(result.Sessions, targetJWT)
	if e != nil {
		return nil, errBehaviorReview
	}
	s, a, e := stableBehaviorLedger(ctx, files, p.OperationID, now)
	if e != nil || a.diagnostic.Status != "responses_observed" || checkHandoverSnapshot(p, s.base.control) != nil || proof.plan != p.Digest || proof.consent != a.intent.ConsentID || proof.records != s.base.control.tokens || proof.runtime != a.intent.RuntimeSHA || a.intent.TargetID != p.TargetID || len(a.known) != len(result.Sessions) {
		return nil, errBehaviorReview
	}
	for i, session := range result.Sessions {
		if a.known[i] != session || session.UserID != proof.users[i] {
			return nil, errBehaviorReview
		}
	}
	oldTokens := transport.oldTokenWitnessSnapshot(sourceJWT, targetJWT)
	reader := transport.readerWitnessSnapshot(consent, targetJWT)
	if reader.authorized && (reader.drifted || len(reader.users) != len(result.Sessions)) {
		return nil, errBehaviorReview
	}
	return &confirmedBehaviorRun{plan: p.Digest, consent: proof.consent, ledger: behaviorLedgerDigest(s), certificate: certificate, identity: handoverTargetIdentity{proof.database, proof.runtime, proof.redis}, records: proof.records, sessions: append([]behaviorSessionResponse(nil), result.Sessions...), windowStart: a.intent.Issued, started: a.intent.Recorded, finished: now, expires: a.intent.Expires, oldTokens: oldTokens, reader: reader}, nil
}

func (g *restoreGuard) attachOldTokenWitness(user uuid.UUID, isAccess bool, w oldTokenProbeWitness, targetKey, sourceKey []byte) {
	if g == nil || user == uuid.Nil || !w.confirmed {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	run := g.behaviorRun
	if g.locations == nil || run == nil || run.reviewStarted || run.reviewSHA != "" || run.oldTokens.drifted {
		return
	}
	if !bytes.Equal(run.targetJWT, targetKey) || (isAccess && (len(sourceKey) < 32 || !bytes.Equal(run.sourceJWT, sourceKey))) {
		run.oldTokens.drifted = true
		return
	}
	if w.plan != run.plan || w.consent != run.consent || w.records != run.records || w.database != run.identity.Database || w.runtime != run.identity.Runtime || w.certificate != run.certificate || !validStamp(w.observedAt) || !validStamp(w.tokenExpires) || w.observedAt.Before(run.windowStart) || !w.observedAt.Before(run.expires) {
		run.oldTokens.drifted = true
		return
	}
	found := false
	for _, s := range run.sessions {
		if s.UserID == user {
			found = true
			break
		}
	}
	if !found {
		run.oldTokens.drifted = true
		return
	}
	if isAccess {
		if run.oldTokens.access == nil {
			run.oldTokens.access = map[uuid.UUID]oldTokenProbeWitness{}
		}
		run.oldTokens.access[user] = w
		return
	}
	if run.oldTokens.refresh == nil {
		run.oldTokens.refresh = map[uuid.UUID]oldTokenProbeWitness{}
	}
	run.oldTokens.refresh[user] = w
}

type boundBehaviorReview struct {
	Format         string                 `json:"report_format"`
	Version        int                    `json:"report_version"`
	Profile        string                 `json:"profile"`
	Scope          string                 `json:"scope"`
	Status         string                 `json:"status"`
	OperationID    string                 `json:"operation_id"`
	BackupID       string                 `json:"backup_id"`
	ConsentID      string                 `json:"consent_id"`
	PlanDigest     string                 `json:"plan_digest"`
	PackageSHA     string                 `json:"package_sha256"`
	ManifestSHA    string                 `json:"manifest_sha256"`
	TargetID       string                 `json:"target_instance_id"`
	Storage        string                 `json:"target_storage_fingerprint"`
	Tree           string                 `json:"publication_tree_fingerprint"`
	Generation     int64                  `json:"observed_generation"`
	OperationSHA   string                 `json:"operation_record_sha256"`
	AttemptSHA     string                 `json:"attempt_record_sha256"`
	PublicationSHA string                 `json:"publication_record_sha256"`
	Identity       handoverTargetIdentity `json:"target_identity"`
	Certificate    string                 `json:"tls_certificate_sha256"`
	Ledger         string                 `json:"behavior_ledger_sha256"`
	AccountCount   int                    `json:"account_count"`
	SessionCount   int                    `json:"session_count"`
	Started        time.Time              `json:"started_at"`
	Finished       time.Time              `json:"finished_at"`
	Expires        time.Time              `json:"expires_at"`
	Checks         []handoverReviewCheck  `json:"checks"`
	Execution      bool                   `json:"execution_authorized"`
	Ready          bool                   `json:"ready_for_cutover"`
	Deployment     bool                   `json:"deployment_authorized"`
}

func boundBehaviorCheckIDs() []string {
	return []string{"schema_ledger", "database_identity", "database_role", "published_objects", "header_keyring", "redis_isolation", "sessions_revoked", "tasks_disabled", "credential_preflight", "https_login_logout", "old_token_http_rejection", "reader_roundtrip"}
}
func (r boundBehaviorReview) validate() error {
	if r.Format != "ylreader-bound-behavior-review" || r.Version != 1 || r.Profile != "bound-https-account-verification-v1" || r.Scope != "localfs-bound-https-account-review" || (r.Status != "partial" && r.Status != "complete") || !validUUID(r.OperationID) || !validUUID(r.BackupID) || !validUUID(r.ConsentID) || !validUUID(r.TargetID) || !r.Identity.valid() || r.Generation < 1 || r.Generation > 9007199254740991 || r.AccountCount < 1 || r.AccountCount > 8 || r.SessionCount != r.AccountCount || r.Execution || r.Ready || r.Deployment || !validStamp(r.Started) || !validStamp(r.Finished) || !validStamp(r.Expires) || r.Finished.Before(r.Started) || r.Finished.Sub(r.Started) > 5*time.Minute || !r.Expires.After(r.Finished) || r.Expires.Sub(r.Finished) > 5*time.Minute {
		return errBehaviorReview
	}
	for _, hash := range []string{r.PlanDigest, r.PackageSHA, r.ManifestSHA, r.Storage, r.Tree, r.OperationSHA, r.AttemptSHA, r.PublicationSHA, r.Certificate, r.Ledger} {
		if !validSHA(hash) {
			return errBehaviorReview
		}
	}
	ids := boundBehaviorCheckIDs()
	if len(r.Checks) != len(ids) {
		return errBehaviorReview
	}
	for i, c := range r.Checks {
		if i >= 10 {
			if c.ID != ids[i] || (c.Status != "unavailable" && c.Status != "pass") {
				return errBehaviorReview
			}
			continue
		}
		if c.ID != ids[i] || c.Status != "pass" {
			return errBehaviorReview
		}
	}
	allPass := r.Checks[10].Status == "pass" && r.Checks[11].Status == "pass"
	if (r.Status == "complete") != allPass {
		return errBehaviorReview
	}
	return nil
}
func loadBoundBehaviorReview(reader io.Reader) (boundBehaviorReview, error) {
	var r boundBehaviorReview
	if reader == nil {
		return r, errBehaviorReview
	}
	data, e := io.ReadAll(io.LimitReader(reader, 32769))
	if e != nil || len(data) > 32768 || exactJSON(data, &r) != nil || r.validate() != nil {
		return boundBehaviorReview{}, errBehaviorReview
	}
	return r, nil
}

func oldTokenRejectionConfirmed(p Plan, run *confirmedBehaviorRun, s behaviorLedgerSnapshot, a behaviorLedgerAssessment, actual handoverTargetIdentity, now time.Time) bool {
	if run == nil || run.oldTokens.drifted || len(a.intent.Users) == 0 || len(a.intent.Users) != len(run.sessions) || len(run.oldTokens.access) != len(a.intent.Users) || len(run.oldTokens.refresh) != len(a.intent.Users) {
		return false
	}
	for _, uidStr := range a.intent.Users {
		uid, e := uuid.Parse(uidStr)
		if e != nil || uid == uuid.Nil {
			return false
		}
		acc, okA := run.oldTokens.access[uid]
		ref, okR := run.oldTokens.refresh[uid]
		if !okA || !okR {
			return false
		}
		for _, w := range []oldTokenProbeWitness{acc, ref} {
			if !w.confirmed || w.plan != p.Digest || w.plan != run.plan || w.consent != a.intent.ConsentID || w.consent != run.consent || w.records != s.base.control.tokens || w.records != run.records || w.database != actual.Database || w.database != run.identity.Database || w.runtime != actual.Runtime || w.runtime != run.identity.Runtime || w.certificate != run.certificate || !validSHA(w.certificate) || !validStamp(w.observedAt) || !validStamp(w.tokenExpires) || w.observedAt.Before(a.intent.Issued) || !w.observedAt.Before(run.expires) || w.observedAt.After(now) || now.Sub(w.observedAt) > 5*time.Minute || !now.Before(w.tokenExpires) {
				return false
			}
		}
	}
	return true
}

func readerRoundtripConfirmed(p Plan, run *confirmedBehaviorRun, s behaviorLedgerSnapshot, a behaviorLedgerAssessment, actual handoverTargetIdentity, now time.Time) bool {
	if run == nil || !run.reader.authorized || run.reader.drifted || len(a.intent.Users) == 0 || len(a.intent.Users) != len(run.sessions) || len(run.reader.users) != len(a.intent.Users) {
		return false
	}
	for i, uidStr := range a.intent.Users {
		uid, e := uuid.Parse(uidStr)
		if e != nil || uid == uuid.Nil || run.sessions[i].UserID != uid || run.sessions[i].SessionID == uuid.Nil {
			return false
		}
		w, ok := run.reader.users[uid]
		if !ok || !w.confirmed || w.sessionID != run.sessions[i].SessionID || w.plan != p.Digest || w.plan != run.plan || w.consent != a.intent.ConsentID || w.consent != run.consent || w.records != s.base.control.tokens || w.records != run.records || w.database != actual.Database || w.database != run.identity.Database || w.runtime != actual.Runtime || w.runtime != run.identity.Runtime || w.certificate != run.certificate || !validSHA(w.certificate) || w.bookID == uuid.Nil || w.otherBookID == uuid.Nil || w.bookID == w.otherBookID || !validSHA(w.beforeDigest) || !validSHA(w.afterDigest) || w.beforeDigest != w.afterDigest || !validStamp(w.observedAt) || w.observedAt.Before(a.intent.Issued) || w.observedAt.Before(run.windowStart) || !w.observedAt.Before(run.expires) || w.observedAt.After(now) || now.Sub(w.observedAt) > 5*time.Minute {
			return false
		}
	}
	return true
}

func checkConfirmedBehaviorRun(p Plan, run *confirmedBehaviorRun, s behaviorLedgerSnapshot, a behaviorLedgerAssessment, now time.Time) error {
	if run == nil || p.Validate() != nil || checkHandoverSnapshot(p, s.base.control) != nil || a.diagnostic.Status != "responses_observed" || !validStamp(now) || run.plan != p.Digest || run.consent != a.intent.ConsentID || run.records != s.base.control.tokens || run.ledger != behaviorLedgerDigest(s) || !validSHA(run.certificate) || !run.identity.valid() || run.identity.Runtime != a.intent.RuntimeSHA || a.intent.TargetID != p.TargetID || now.Before(run.finished) || !now.Before(run.expires) || run.started != a.intent.Recorded || run.expires != a.intent.Expires || len(run.sessions) != len(a.known) || len(run.sessions) != len(a.intent.Users) {
		return errBehaviorReview
	}
	for i, session := range run.sessions {
		if session != a.known[i] || session.UserID.String() != a.intent.Users[i] || session.UserID == uuid.Nil || session.SessionID == uuid.Nil {
			return errBehaviorReview
		}
	}
	return nil
}

func (g *restoreGuard) collectBoundBehaviorReview(ctx context.Context, inputs handoverProbeInputs, clock func() time.Time) (boundBehaviorReview, error) {
	if g == nil || ctx == nil || clock == nil || inputs.Guard != g {
		return boundBehaviorReview{}, errBehaviorReview
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	g.mu.Lock()
	if g.locations == nil || g.behaviorRun == nil || g.behaviorRun.reviewStarted || g.behaviorRun.reviewSHA != "" {
		g.mu.Unlock()
		return boundBehaviorReview{}, errBehaviorReview
	}
	if len(g.behaviorRun.sourceJWT) < 32 || len(g.behaviorRun.targetJWT) < 32 || !bytes.Equal(inputs.SourceJWT, g.behaviorRun.sourceJWT) || !bytes.Equal(inputs.TargetJWT, g.behaviorRun.targetJWT) {
		g.mu.Unlock()
		return boundBehaviorReview{}, errBehaviorReview
	}
	run := g.behaviorRun
	run.reviewStarted = true
	p := g.plan
	files := g.locations.files[3]
	g.mu.Unlock()
	// Collection is read-only: a failed observation may be explicitly retried
	// within the same witness window. Neither dispatch nor a write is retried.
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.behaviorRun == run && run.reviewSHA == "" {
			run.reviewStarted = false
		}
	}()
	s, a, e := stableBehaviorLedger(ctx, files, p.OperationID, clock())
	if e != nil || inputs.OwnerID != s.base.control.op.OwnerID || checkConfirmedBehaviorRun(p, run, s, a, clock()) != nil {
		return boundBehaviorReview{}, errBehaviorReview
	}
	probes, e := inputs.probes(p)
	if e != nil {
		return boundBehaviorReview{}, errBehaviorReview
	}
	var actual handoverTargetIdentity
	for _, probe := range []struct {
		run func(context.Context) (string, error)
		out *string
	}{{probes.Database, &actual.Database}, {probes.Runtime, &actual.Runtime}, {probes.Redis, &actual.Redis}} {
		value, e := probe.run(ctx)
		if e != nil {
			return boundBehaviorReview{}, errBehaviorReview
		}
		*probe.out = value
	}
	if actual != run.identity {
		return boundBehaviorReview{}, errBehaviorReview
	}
	observed := observeBehaviorSessions(ctx, files, p, inputs.DatabaseURL, inputs.SourceDatabase, inputs.TargetDatabase, clock())
	if observed.Database != "known_sessions_revoked" || observed.Known != len(run.sessions) || observed.Revoked != len(run.sessions) || observed.Active != 0 || observed.Missing != 0 || observed.Foreign != 0 || observed.Unaccounted != 0 {
		return boundBehaviorReview{}, errBehaviorReview
	}
	allowance := handoverSessionAllowance{start: run.started.Add(-time.Second), end: run.finished}
	for _, session := range run.sessions {
		allowance.ids = append(allowance.ids, session.SessionID)
		allowance.users = append(allowance.users, session.UserID)
	}
	dataProof, e := g.probeHandoverDataSessions(ctx, inputs.Stage, inputs.OwnerID, p.TargetID, clock(), inputs.Ring, inputs.RequiredKIDs, s.base.control, &allowance)
	if e != nil || !dataProof.matches(p, s.base.control) {
		return boundBehaviorReview{}, errBehaviorReview
	}
	after, assessed, e := stableBehaviorLedger(ctx, files, p.OperationID, clock())
	now := clock()
	if e != nil || !s.equal(after) || checkConfirmedBehaviorRun(p, run, after, assessed, now) != nil || ctx.Err() != nil {
		return boundBehaviorReview{}, errBehaviorReview
	}
	expires := run.expires
	planExpiry, _ := time.Parse(time.RFC3339Nano, p.Expires)
	if planExpiry.Before(expires) {
		expires = planExpiry
	}
	if now.Add(5 * time.Minute).Before(expires) {
		expires = now.Add(5 * time.Minute)
	}
	oldTokensPass := oldTokenRejectionConfirmed(p, run, after, assessed, actual, now)
	readerPass := readerRoundtripConfirmed(p, run, after, assessed, actual, now)
	reportStatus := "partial"
	if oldTokensPass && readerPass {
		reportStatus = "complete"
	}
	r := boundBehaviorReview{Format: "ylreader-bound-behavior-review", Version: 1, Profile: "bound-https-account-verification-v1", Scope: "localfs-bound-https-account-review", Status: reportStatus, OperationID: p.OperationID, BackupID: p.BackupID, ConsentID: run.consent, PlanDigest: p.Digest, PackageSHA: p.PackageSHA, ManifestSHA: p.ManifestSHA, TargetID: p.TargetID, Storage: p.TargetFingerprint, Tree: after.base.control.pub.TreeFingerprint, Generation: after.base.control.op.Generation, OperationSHA: run.records[0], AttemptSHA: run.records[1], PublicationSHA: run.records[2], Identity: actual, Certificate: run.certificate, Ledger: run.ledger, AccountCount: len(run.sessions), SessionCount: len(run.sessions), Started: run.started, Finished: now, Expires: expires}
	for i, id := range boundBehaviorCheckIDs() {
		status := "pass"
		if i == 10 && !oldTokensPass {
			status = "unavailable"
		} else if i == 11 && !readerPass {
			status = "unavailable"
		}
		r.Checks = append(r.Checks, handoverReviewCheck{id, status})
	}
	if r.validate() != nil {
		return boundBehaviorReview{}, errBehaviorReview
	}
	encoded, _ := json.Marshal(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil || g.behaviorRun != run {
		return boundBehaviorReview{}, errBehaviorReview
	}
	if !bytes.Equal(inputs.SourceJWT, run.sourceJWT) || !bytes.Equal(inputs.TargetJWT, run.targetJWT) {
		return boundBehaviorReview{}, errBehaviorReview
	}
	run.reviewSHA = digestBytes(encoded)
	clear(run.sourceJWT)
	clear(run.targetJWT)
	return r, nil
}

func boundBehaviorReviewKey(id string) (string, error) {
	if !validUUID(id) {
		return "", errBehaviorReview
	}
	return "operations/" + id + "/behavior-account-review.json", nil
}
func saveBoundBehaviorReview(ctx context.Context, files storage.ConditionalFiles, r boundBehaviorReview) error {
	if ctx == nil || files == nil || r.validate() != nil || ctx.Err() != nil {
		return errBehaviorReview
	}
	key, _ := boundBehaviorReviewKey(r.OperationID)
	data, _ := json.Marshal(r)
	if files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data))) != nil {
		return errBehaviorReview
	}
	back, present, e := optionalStatusBytes(ctx, files, key, 32768)
	if e != nil || !present || !bytes.Equal(back, data) || ctx.Err() != nil {
		return errBehaviorReview
	}
	return nil
}
func (g *restoreGuard) persistBoundBehaviorReview(ctx context.Context, r boundBehaviorReview, clock func() time.Time) error {
	if g == nil || ctx == nil || clock == nil {
		return errBehaviorReview
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	g.mu.Lock()
	defer g.mu.Unlock()
	run := g.behaviorRun
	if g.locations == nil || run == nil || run.writeAttempted {
		return errBehaviorReview
	}
	run.writeAttempted = true
	encoded, _ := json.Marshal(r)
	now := clock()
	if r.validate() != nil || run.reviewSHA != digestBytes(encoded) || now.Before(r.Finished) || !now.Before(r.Expires) {
		return errBehaviorReview
	}
	files := g.locations.files[3]
	s, a, e := stableBehaviorLedger(ctx, files, g.plan.OperationID, now)
	if e != nil || checkConfirmedBehaviorRun(g.plan, run, s, a, now) != nil {
		return errBehaviorReview
	}
	if _, e = g.recheckBindingsLocked(ctx, g.plan.TargetID, now, PostgresTargetInspector{}, false); e != nil {
		return errBehaviorReview
	}
	if saveBoundBehaviorReview(ctx, files, r) != nil {
		return errBehaviorReview
	}
	after, assessed, e := stableBehaviorLedger(ctx, files, g.plan.OperationID, clock())
	if e != nil || !s.equal(after) || checkConfirmedBehaviorRun(g.plan, run, after, assessed, clock()) != nil || !clock().Before(r.Expires) || ctx.Err() != nil {
		return errBehaviorReview
	}
	return nil
}
