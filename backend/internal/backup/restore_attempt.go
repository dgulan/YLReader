package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"

	"yreader/backend/internal/pkg/storage"
)

var ErrAttempt = errors.New("backup: invalid or conflicting restore attempt")
var generationReference = regexp.MustCompile(`^\.scratch-[0-9a-f]{32}$`)

type restoreAttempt struct {
	Format            string  `json:"attempt_format"`
	Version           int     `json:"attempt_version"`
	OperationID       string  `json:"operation_id"`
	BackupID          string  `json:"backup_id"`
	PlanDigest        string  `json:"plan_digest"`
	OwnerID           string  `json:"owner_id"`
	TargetFingerprint string  `json:"target_storage_fingerprint"`
	Generation        *string `json:"generation_reference"`
	DatabaseOutcome   string  `json:"database_outcome"`
}

func (a restoreAttempt) validate() error {
	if a.Format != "ylreader-restore-attempt" || a.Version != 1 || !validUUID(a.OperationID) || !validUUID(a.BackupID) || !validUUID(a.OwnerID) || !validSHA(a.PlanDigest) || !validSHA(a.TargetFingerprint) {
		return ErrAttempt
	}
	if a.Generation != nil && !generationReference.MatchString(*a.Generation) {
		return ErrAttempt
	}
	switch a.DatabaseOutcome {
	case "not_started":
	case "unknown", "committed", "rolled_back":
		if a.Generation == nil {
			return ErrAttempt
		}
	default:
		return ErrAttempt
	}
	return nil
}

func attemptTransition(old, next restoreAttempt) error {
	if old.validate() != nil || next.validate() != nil {
		return ErrAttempt
	}
	a, b := old, next
	a.Generation = nil
	b.Generation = nil
	a.DatabaseOutcome = ""
	b.DatabaseOutcome = ""
	if a != b {
		return ErrAttempt
	}
	if old.Generation != nil && (next.Generation == nil || *next.Generation != *old.Generation) {
		return ErrAttempt
	}
	if old.Generation == nil && next.Generation != nil && (old.DatabaseOutcome != "not_started" || next.DatabaseOutcome != "not_started") {
		return ErrAttempt
	}
	if old.DatabaseOutcome == next.DatabaseOutcome {
		return nil
	}
	if old.DatabaseOutcome == "not_started" && next.DatabaseOutcome == "unknown" && old.Generation != nil {
		return nil
	}
	if old.DatabaseOutcome == "unknown" && (next.DatabaseOutcome == "committed" || next.DatabaseOutcome == "rolled_back") {
		return nil
	}
	return ErrAttempt
}

type attemptStore struct{ Files storage.ConditionalFiles }

func attemptKey(id string) (string, error) {
	if !validUUID(id) {
		return "", ErrAttempt
	}
	return "operations/" + id + "/attempt.json", nil
}

func (s attemptStore) Create(ctx context.Context, a restoreAttempt) error {
	if s.Files == nil || a.validate() != nil || a.Generation != nil || a.DatabaseOutcome != "not_started" {
		return ErrAttempt
	}
	key, _ := attemptKey(a.OperationID)
	data, _ := json.Marshal(a)
	return s.Files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data)))
}

func decodeAttempt(data []byte) (restoreAttempt, error) {
	var a restoreAttempt
	if len(data) > 4096 || !utf8.Valid(data) || !pairedUnicodeEscapes(data) {
		return a, ErrAttempt
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return a, ErrAttempt
	}
	seen := map[string]bool{}
	for d.More() {
		token, e = d.Token()
		key, ok := token.(string)
		if e != nil || !ok || seen[key] {
			return a, ErrAttempt
		}
		seen[key] = true
		switch key {
		case "attempt_format", "attempt_version", "operation_id", "backup_id", "plan_digest", "owner_id", "target_storage_fingerprint", "generation_reference", "database_outcome":
		default:
			return a, ErrAttempt
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || (bytes.Equal(bytes.TrimSpace(value), []byte("null")) && key != "generation_reference") {
			return a, ErrAttempt
		}
	}
	if _, e = d.Token(); e != nil || len(seen) != 9 {
		return a, ErrAttempt
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.validate() != nil {
		return restoreAttempt{}, ErrAttempt
	}
	return a, nil
}

func (s attemptStore) Load(ctx context.Context, id string) (restoreAttempt, string, error) {
	key, e := attemptKey(id)
	if e != nil || s.Files == nil {
		return restoreAttempt{}, "", ErrAttempt
	}
	r, e := s.Files.Get(ctx, key)
	if e != nil {
		return restoreAttempt{}, "", e
	}
	data, e := io.ReadAll(io.LimitReader(r, 4097))
	closed := r.Close()
	if e != nil || closed != nil {
		return restoreAttempt{}, "", ErrAttempt
	}
	a, e := decodeAttempt(data)
	if e != nil || a.OperationID != id {
		return restoreAttempt{}, "", ErrAttempt
	}
	hash := sha256.Sum256(data)
	return a, hex.EncodeToString(hash[:]), nil
}

func (s attemptStore) Update(ctx context.Context, id, token string, next restoreAttempt) error {
	old, current, e := s.Load(ctx, id)
	if e != nil {
		return e
	}
	if current != token {
		return storage.ErrConflict
	}
	if attemptTransition(old, next) != nil {
		return ErrAttempt
	}
	data, _ := json.Marshal(next)
	key, _ := attemptKey(id)
	return s.Files.CompareAndSwap(ctx, key, token, data)
}
