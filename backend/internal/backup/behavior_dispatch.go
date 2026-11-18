package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/storage"
)

var errBehaviorDispatch = errors.New("backup: behavior dispatch blocked or outcome unconfirmed; do not retry login")

type behaviorTransportBinding struct{ TargetID, Database, Runtime string }
type behaviorSessionResponse struct{ UserID, SessionID uuid.UUID }

// Trusted internal adapter only. Binding is an assertion, not remote identity
// evidence. No external URL/plugin adapter or production HTTP client is exposed.
type behaviorLoginTransport interface {
	Binding() behaviorTransportBinding
	Login(context.Context, behaviorCredential) (behaviorSessionResponse, error)
	Logout(context.Context, behaviorSessionResponse) error
}

type behaviorAction struct {
	Format      string    `json:"action_format"`
	Version     int       `json:"action_version"`
	OperationID string    `json:"operation_id"`
	ConsentID   string    `json:"consent_id"`
	IntentSHA   string    `json:"intent_sha256"`
	Index       int       `json:"index"`
	UserID      string    `json:"user_id"`
	Kind        string    `json:"kind"`
	SessionID   string    `json:"session_id"`
	ParentSHA   string    `json:"parent_sha256"`
	Observed    time.Time `json:"observed_at"`
}

func (a behaviorAction) validate() error {
	if a.Format != "ylreader-behavior-action" || a.Version != 1 || !validUUID(a.OperationID) || !validUUID(a.ConsentID) || !validSHA(a.IntentSHA) || a.Index < 0 || a.Index > 7 || !validUUID(a.UserID) || !validSHA(a.ParentSHA) || !validStamp(a.Observed) {
		return errBehaviorDispatch
	}
	switch a.Kind {
	case "dispatch_reserved", "login_response_unknown":
		if a.SessionID != "" {
			return errBehaviorDispatch
		}
	case "session_observed", "logout_response_observed", "logout_response_unknown":
		if !validUUID(a.SessionID) || a.SessionID == uuid.Nil.String() {
			return errBehaviorDispatch
		}
	default:
		return errBehaviorDispatch
	}
	return nil
}
func (a behaviorAction) key() (string, error) {
	if a.validate() != nil {
		return "", errBehaviorDispatch
	}
	file := "response.json"
	if a.Kind == "dispatch_reserved" {
		file = "dispatch.json"
	} else if a.Kind == "logout_response_observed" || a.Kind == "logout_response_unknown" {
		file = "logout.json"
	}
	return "operations/" + a.OperationID + "/behavior-actions/" + strconv.Itoa(a.Index) + "/" + file, nil
}
func persistBehaviorAction(ctx context.Context, files storage.ConditionalFiles, a behaviorAction) (string, error) {
	key, e := a.key()
	if ctx == nil || files == nil || e != nil || ctx.Err() != nil {
		return "", errBehaviorDispatch
	}
	data, _ := json.Marshal(a)
	if files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data))) != nil {
		return "", errBehaviorDispatch
	}
	back, present, e := optionalStatusBytes(ctx, files, key, 8192)
	var parsed behaviorAction
	if e != nil || !present || !bytes.Equal(back, data) || exactJSON(back, &parsed) != nil || parsed.validate() != nil || ctx.Err() != nil {
		return "", errBehaviorDispatch
	}
	return digestBytes(data), nil
}

// A response summary only, never a report proof or authorization to resume.
type behaviorDispatchObservation struct {
	Sessions          []behaviorSessionResponse
	ResponsesObserved bool
	Stage             string
}

func (behaviorDispatchObservation) MarshalJSON() ([]byte, error) { return nil, errBehaviorDispatch }
func (behaviorDispatchObservation) String() string {
	return "behavior response observation; no execution authority"
}

func checkBehaviorDispatchInput(g *restoreGuard, p Plan, prepared *preparedBehaviorInput, proof *verifiedBehaviorAccounts, binding behaviorTransportBinding, now time.Time) error {
	if prepared == nil || proof == nil || g == nil || proof.guard != g || p.Validate() != nil || prepared.consent.validate() != nil || proof.plan != p.Digest || proof.consent != prepared.consent.ID || proof.runtime != prepared.consent.RuntimeSHA || !validSHA(proof.database) || proof.records != [3]string{prepared.consent.OperationSHA, prepared.consent.AttemptSHA, prepared.consent.PublicationSHA} || binding != (behaviorTransportBinding{p.TargetID, proof.database, proof.runtime}) || len(proof.users) != len(prepared.accounts) || len(prepared.accounts) != len(prepared.consent.Accounts) || len(proof.users) == 0 || !validStamp(now) || now.Before(proof.checked) || now.Sub(proof.checked) > 20*time.Second || !now.Before(proof.expires) || now.Before(prepared.consent.Issued) || !now.Before(prepared.consent.Expires) {
		return errBehaviorDispatch
	}
	for i, id := range proof.users {
		if id == uuid.Nil || id.String() != prepared.consent.Accounts[i].UserID || !prepared.accounts[i].valid(id.String()) {
			return errBehaviorDispatch
		}
	}
	return nil
}

// Caller holds live guard/exclusivity. Separated only for deterministic fault
// tests; no external callback registration or reconstructed proof is accepted.
func dispatchBehaviorCore(ctx context.Context, files storage.ConditionalFiles, g *restoreGuard, p Plan, prepared *preparedBehaviorInput, proof *verifiedBehaviorAccounts, transport behaviorLoginTransport, clock func() time.Time, recheck func(context.Context) error) (behaviorDispatchObservation, error) {
	result := behaviorDispatchObservation{Stage: "core_input"}
	if ctx == nil || files == nil || transport == nil || clock == nil || recheck == nil {
		return result, errBehaviorDispatch
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	check := func() error {
		if ctx.Err() != nil || checkBehaviorDispatchInput(g, p, prepared, proof, transport.Binding(), clock()) != nil || recheck(ctx) != nil {
			return errBehaviorDispatch
		}
		return nil
	}
	result.Stage = "pre_intent_recheck"
	if check() != nil {
		return result, errBehaviorDispatch
	}
	result.Stage = "intent_record"
	intent, e := recordBehaviorIntent(ctx, files, prepared, p, proof.runtime, clock)
	if e != nil {
		return result, errBehaviorDispatch
	}
	data, _ := json.Marshal(intent)
	intentSHA := digestBytes(data)
	seen := map[uuid.UUID]bool{}
	for index, credential := range prepared.accounts {
		result.Stage = "pre_dispatch_recheck"
		if check() != nil {
			return result, errBehaviorDispatch
		}
		a := behaviorAction{Format: "ylreader-behavior-action", Version: 1, OperationID: p.OperationID, ConsentID: prepared.consent.ID, IntentSHA: intentSHA, Index: index, UserID: credential.UserID, Kind: "dispatch_reserved", ParentSHA: intentSHA, Observed: clock()}
		result.Stage = "dispatch_action_persist"
		dispatchSHA, e := persistBehaviorAction(ctx, files, a)
		if e != nil || check() != nil {
			return result, errBehaviorDispatch
		}
		// Exactly one call; even a nil/invalid response is an unknown outcome.
		credential.OldAccess, credential.OldRefresh = "", ""
		result.Stage = "login_transport"
		response, loginErr := transport.Login(ctx, credential)
		a.ParentSHA = dispatchSHA
		a.Observed = clock()
		a.Kind = "login_response_unknown"
		if loginErr != nil || response.UserID != proof.users[index] || response.SessionID == uuid.Nil || seen[response.SessionID] {
			_, _ = persistBehaviorAction(ctx, files, a)
			return result, errBehaviorDispatch
		}
		seen[response.SessionID] = true
		result.Sessions = append(result.Sessions, response)
		a.Kind = "session_observed"
		a.SessionID = response.SessionID.String()
		// Consent explicitly includes one bounded logout of an already known
		// same-user session after cancellation. Never log in on this context.
		result.Stage = "post_login_persist_or_logout"
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		responseSHA, responseErr := persistBehaviorAction(cleanup, files, a)
		logoutErr := transport.Logout(cleanup, response)
		if responseSHA == "" {
			encoded, _ := json.Marshal(a)
			responseSHA = digestBytes(encoded)
		}
		a.ParentSHA = responseSHA
		a.Observed = clock()
		a.Kind = "logout_response_observed"
		if logoutErr != nil {
			a.Kind = "logout_response_unknown"
		}
		_, writeErr := persistBehaviorAction(cleanup, files, a)
		stop()
		if responseErr != nil || logoutErr != nil || writeErr != nil || ctx.Err() != nil || check() != nil {
			return result, errBehaviorDispatch
		}
	}
	result.ResponsesObserved = true
	result.Stage = "completed"
	return result, nil
}

func (g *restoreGuard) dispatchBehaviorOnce(ctx context.Context, prepared *preparedBehaviorInput, inputs handoverProbeInputs, transport behaviorLoginTransport, clock func() time.Time) (behaviorDispatchObservation, error) {
	empty := behaviorDispatchObservation{Stage: "guard_preflight"}
	if g == nil || ctx == nil || transport == nil || clock == nil || len(inputs.SourceJWT) < 32 || len(inputs.SourceJWT) > 4096 || len(inputs.TargetJWT) < 32 || len(inputs.TargetJWT) > 4096 {
		return empty, errBehaviorDispatch
	}
	g.mu.Lock()
	if g.behaviorStarted || g.locations == nil {
		g.mu.Unlock()
		empty.Stage = "guard_replay_or_closed"
		return empty, errBehaviorDispatch
	}
	g.behaviorStarted = true
	g.mu.Unlock()
	sourceKey, targetKey := append([]byte(nil), inputs.SourceJWT...), append([]byte(nil), inputs.TargetJWT...)
	inputs.SourceJWT, inputs.TargetJWT = sourceKey, targetKey
	transferred := false
	defer func() {
		if !transferred {
			clear(sourceKey)
			clear(targetKey)
		}
	}()
	proof, e := g.verifyBehaviorAccounts(ctx, prepared, inputs, clock)
	if e != nil {
		empty.Stage = "account_verification:" + g.accountStageValue()
		return empty, errBehaviorDispatch
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil {
		empty.Stage = "guard_closed"
		return empty, errBehaviorDispatch
	}
	recheck := func(ctx context.Context) error {
		if _, e := g.recheckBindingsLocked(ctx, g.plan.TargetID, clock(), PostgresTargetInspector{}, false); e != nil {
			return errBehaviorDispatch
		}
		s, e := stableHandoverSnapshot(ctx, g.locations.files[3], g.plan.OperationID)
		if e != nil || s.tokens != proof.records || prepared.consent.matches(g.plan, s, proof.runtime, clock()) != nil {
			return errBehaviorDispatch
		}
		return nil
	}
	result, err := dispatchBehaviorCore(ctx, g.locations.files[3], g, g.plan, prepared, proof, transport, clock, recheck)
	if err == nil {
		if concrete, ok := transport.(*behaviorHTTPTransport); ok {
			run, e := captureConfirmedBehaviorRun(ctx, g.locations.files[3], g.plan, proof, concrete, result, targetKey, clock())
			if e != nil {
				result.Stage = "confirmed_witness_capture"
				return result, errBehaviorDispatch
			}
			run.sourceJWT, run.targetJWT = sourceKey, targetKey
			transferred = true
			g.behaviorRun = run
		}
	}
	return result, err
}

var _ fmt.Stringer = behaviorDispatchObservation{}
