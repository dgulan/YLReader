package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

var errBehaviorConsent = errors.New("backup: behavior consent input unavailable or changed")

type behaviorConsentAccount struct {
	UserID        string `json:"user_id"`
	CredentialKey string `json:"credential_key"`
}

// This is operator intent only, never a behaviorGrant or restore capability.
type behaviorConsent struct {
	Format         string                   `json:"consent_format"`
	Version        int                      `json:"consent_version"`
	Scope          string                   `json:"scope"`
	ID             string                   `json:"consent_id"`
	OperationID    string                   `json:"operation_id"`
	OwnerID        string                   `json:"owner_id"`
	PlanDigest     string                   `json:"plan_digest"`
	TargetID       string                   `json:"target_instance_id"`
	Generation     int64                    `json:"observed_generation"`
	OperationSHA   string                   `json:"operation_record_sha256"`
	AttemptSHA     string                   `json:"attempt_record_sha256"`
	PublicationSHA string                   `json:"publication_record_sha256"`
	RuntimeSHA     string                   `json:"runtime_config_revision_sha256"`
	Issued         time.Time                `json:"issued_at"`
	Expires        time.Time                `json:"expires_at"`
	Accounts       []behaviorConsentAccount `json:"accounts"`
}

func (a behaviorConsent) validate() error {
	if a.Format != "ylreader-behavior-consent" || a.Version != 1 || a.Scope != "isolated-account-verification-only" || !validUUID(a.ID) || !validUUID(a.OperationID) || !validUUID(a.OwnerID) || !validUUID(a.TargetID) || !validSHA(a.PlanDigest) || !validSHA(a.OperationSHA) || !validSHA(a.AttemptSHA) || !validSHA(a.PublicationSHA) || !validSHA(a.RuntimeSHA) || a.Generation < 1 || a.Generation > 9007199254740991 || !validStamp(a.Issued) || !validStamp(a.Expires) || !a.Expires.After(a.Issued) || a.Expires.Sub(a.Issued) > 5*time.Minute || len(a.Accounts) < 1 || len(a.Accounts) > 8 {
		return errBehaviorConsent
	}
	users, keys := map[string]bool{}, map[string]bool{}
	for _, u := range a.Accounts {
		if !validUUID(u.UserID) || !safeKey(u.CredentialKey) || users[u.UserID] || keys[u.CredentialKey] {
			return errBehaviorConsent
		}
		users[u.UserID] = true
		keys[u.CredentialKey] = true
	}
	return nil
}

func (a behaviorConsent) matches(p Plan, s diagnosticSnapshot, runtimeSHA string, now time.Time) error {
	if a.validate() != nil || checkHandoverSnapshot(p, s) != nil || !validSHA(runtimeSHA) || !validStamp(now) || now.Before(a.Issued) || !now.Before(a.Expires) {
		return errBehaviorConsent
	}
	created, _ := time.Parse(time.RFC3339Nano, p.Created)
	expires, _ := time.Parse(time.RFC3339Nano, p.Expires)
	if a.Issued.Before(created) || a.Issued.Before(s.op.UpdatedAt) || a.Expires.After(expires) || a.OperationID != p.OperationID || a.OwnerID != s.op.OwnerID || a.PlanDigest != p.Digest || a.TargetID != p.TargetID || a.Generation != s.op.Generation || a.RuntimeSHA != runtimeSHA || [3]string{a.OperationSHA, a.AttemptSHA, a.PublicationSHA} != s.tokens {
		return errBehaviorConsent
	}
	return nil
}

type behaviorCredential struct {
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	OldAccess  string `json:"old_access_token"`
	OldRefresh string `json:"old_refresh_token"`
}

func (behaviorCredential) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[redacted behavior credential]")
}
func (behaviorCredential) MarshalJSON() ([]byte, error) { return nil, errBehaviorConsent }
func (c behaviorCredential) valid(user string) bool {
	return validUUID(user) && c.UserID == user && len(c.Username) > 0 && len(c.Username) <= 256 && len(c.Password) > 0 && len(c.Password) <= 4096 && len(c.OldAccess) > 0 && len(c.OldAccess) <= 16384 && validSHA(c.OldRefresh)
}

// No conversion to behaviorAuthorization/grant or proof is provided.
type preparedBehaviorInput struct {
	consent  behaviorConsent
	accounts []behaviorCredential
}

func (preparedBehaviorInput) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[redacted behavior input; no execution authority]")
}
func (preparedBehaviorInput) MarshalJSON() ([]byte, error) { return nil, errBehaviorConsent }

// Caller owns this input exclusively; Close must not race with access or copies.
func (p *preparedBehaviorInput) Close() {
	if p == nil {
		return
	}
	clear(p.accounts)
	p.accounts = nil
	p.consent = behaviorConsent{}
}

func readBehaviorInput(ctx context.Context, r RestoreRecordReader, key string, limit int64) (data []byte, err error) {
	if ctx == nil || ctx.Err() != nil || r == nil || !safeKey(key) {
		return nil, errBehaviorConsent
	}
	f, e := r.Get(ctx, key)
	if e != nil || f == nil {
		return nil, errBehaviorConsent
	}
	data, e = io.ReadAll(io.LimitReader(f, limit+1))
	closed := f.Close()
	if e != nil || closed != nil || int64(len(data)) > limit || ctx.Err() != nil {
		clear(data)
		return nil, errBehaviorConsent
	}
	return data, nil
}

// Read-only preflight only. Callers must provide safe directory readers and an
// independently checked runtime revision. No network or persistent replay claim.
func prepareBehaviorInput(ctx context.Context, input, control RestoreRecordReader, key string, p Plan, runtimeSHA string, clock func() time.Time) (result *preparedBehaviorInput, err error) {
	if ctx == nil || input == nil || control == nil || clock == nil || !safeKey(key) || !validSHA(runtimeSHA) {
		return nil, errBehaviorConsent
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, e := readBehaviorInput(ctx, input, key, 16<<10)
	if e != nil {
		return nil, errBehaviorConsent
	}
	defer clear(raw)
	var a behaviorConsent
	if exactJSON(raw, &a) != nil || a.validate() != nil {
		return nil, errBehaviorConsent
	}
	s, e := stableHandoverSnapshot(ctx, diagnosticFiles{control}, p.OperationID)
	if e != nil || a.matches(p, s, runtimeSHA, clock()) != nil {
		return nil, errBehaviorConsent
	}
	prepared := &preparedBehaviorInput{consent: a}
	defer func() {
		if err != nil {
			prepared.Close()
		}
	}()
	// Keep bounded original bytes only until all rereads complete. Do not hash secrets.
	originals := make([][]byte, 0, len(a.Accounts))
	defer func() {
		for _, b := range originals {
			clear(b)
		}
	}()
	for _, account := range a.Accounts {
		if account.CredentialKey == key {
			return nil, errBehaviorConsent
		}
		data, e := readBehaviorInput(ctx, input, account.CredentialKey, 32<<10)
		if e != nil {
			return nil, errBehaviorConsent
		}
		originals = append(originals, data)
		var secret behaviorCredential
		if exactJSON(data, &secret) != nil || !secret.valid(account.UserID) {
			return nil, errBehaviorConsent
		}
		prepared.accounts = append(prepared.accounts, secret)
	}
	for i, account := range a.Accounts {
		again, e := readBehaviorInput(ctx, input, account.CredentialKey, 32<<10)
		equal := e == nil && bytes.Equal(again, originals[i])
		clear(again)
		if !equal {
			return nil, errBehaviorConsent
		}
	}
	again, e := readBehaviorInput(ctx, input, key, 16<<10)
	equal := e == nil && bytes.Equal(again, raw)
	clear(again)
	if !equal {
		return nil, errBehaviorConsent
	}
	after, e := stableHandoverSnapshot(ctx, diagnosticFiles{control}, p.OperationID)
	if e != nil || s.tokens != after.tokens || a.matches(p, after, runtimeSHA, clock()) != nil || ctx.Err() != nil {
		return nil, errBehaviorConsent
	}
	return prepared, nil
}

// Compile-time check: secrets have no accidental default JSON representation.
var _ json.Marshaler = behaviorCredential{}
