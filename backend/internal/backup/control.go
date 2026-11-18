package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/storage"
)

var ErrState = errors.New("backup: invalid operation state or transition")

type Operation struct {
	Format          string     `json:"operation_format"`
	Version         int        `json:"operation_version"`
	ID              string     `json:"operation_id"`
	BackupID        string     `json:"backup_id"`
	PlanDigest      string     `json:"plan_digest"`
	Generation      int64      `json:"generation"`
	Phase           string     `json:"phase"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	HeartbeatAt     time.Time  `json:"heartbeat_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	FailureCode     *string    `json:"failure_code"`
	CancelRequested bool       `json:"cancel_requested"`
	OwnerID         string     `json:"owner_id"`
}

func validUUID(s string) bool { u, e := uuid.Parse(s); return e == nil && u.String() == s }
func terminal(s string) bool  { return s == "completed" || s == "failed" || s == "canceled" }
func validStamp(t time.Time) bool {
	_, offset := t.Zone()
	return !t.IsZero() && offset == 0 && t.Nanosecond()%1000 == 0 && t.Year() >= 1 && t.Year() <= 9999
}

func (o Operation) Validate() error {
	if o.Format != "ylreader-restore-operation" || o.Version != 1 || !validUUID(o.ID) || !validUUID(o.BackupID) || !validUUID(o.OwnerID) || o.Generation < 1 || o.Generation > 9007199254740991 {
		return ErrState
	}
	digest, e := hex.DecodeString(o.PlanDigest)
	if e != nil || len(digest) != 32 || hex.EncodeToString(digest) != o.PlanDigest {
		return ErrState
	}
	switch o.Phase {
	case "planned", "staging", "validating", "ready_for_cutover", "completed", "failed", "canceled":
	default:
		return ErrState
	}
	if !validStamp(o.CreatedAt) || !validStamp(o.UpdatedAt) || !validStamp(o.HeartbeatAt) || o.HeartbeatAt.Before(o.CreatedAt) || o.UpdatedAt.Before(o.HeartbeatAt) {
		return ErrState
	}
	if terminal(o.Phase) != (o.FinishedAt != nil) || (o.Phase == "failed") != (o.FailureCode != nil) {
		return ErrState
	}
	if o.FinishedAt != nil && (!validStamp(*o.FinishedAt) || !o.FinishedAt.Equal(o.UpdatedAt)) {
		return ErrState
	}
	if o.FailureCode != nil {
		switch *o.FailureCode {
		case "operation_interrupted", "integrity_failed", "schema_incompatible", "storage_failed", "database_failed", "validation_failed", "internal_error":
		default:
			return ErrState
		}
	}
	if o.Phase == "canceled" && !o.CancelRequested || o.Phase == "completed" && o.CancelRequested {
		return ErrState
	}
	return nil
}

func ValidateTransition(old, next Operation, expected int64, confirmedDead bool) error {
	if old.Validate() != nil || next.Validate() != nil || terminal(old.Phase) || old.Generation != expected || next.Generation != old.Generation+1 {
		return ErrState
	}
	if old.ID != next.ID || old.BackupID != next.BackupID || old.PlanDigest != next.PlanDigest || old.OwnerID != next.OwnerID || !old.CreatedAt.Equal(next.CreatedAt) || !next.UpdatedAt.After(old.UpdatedAt) || next.HeartbeatAt.Before(old.HeartbeatAt) {
		return ErrState
	}
	forward := map[string]string{"planned": "staging", "staging": "validating", "validating": "ready_for_cutover", "ready_for_cutover": "completed"}
	if next.Phase != old.Phase && next.Phase != forward[old.Phase] && next.Phase != "failed" && next.Phase != "canceled" {
		return ErrState
	}
	if old.CancelRequested && (!next.CancelRequested || next.Phase != old.Phase && next.Phase != "failed" && next.Phase != "canceled") {
		return ErrState
	}
	if next.FailureCode != nil && *next.FailureCode == "operation_interrupted" && !confirmedDead {
		return ErrState
	}
	return nil
}

type Control struct{ Files storage.ConditionalFiles }

func operationKey(id string) (string, error) {
	if !validUUID(id) {
		return "", ErrState
	}
	return "operations/" + id + "/state.json", nil
}

func (c Control) Create(ctx context.Context, o Operation) error {
	if c.Files == nil || o.Validate() != nil || o.Phase != "planned" || o.Generation != 1 || o.CancelRequested {
		return ErrState
	}
	key, _ := operationKey(o.ID)
	data, e := json.Marshal(o)
	if e != nil {
		return ErrState
	}
	return c.Files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data)))
}

func (c Control) Load(ctx context.Context, id string) (Operation, string, error) {
	var o Operation
	key, e := operationKey(id)
	if e != nil || c.Files == nil {
		return o, "", ErrState
	}
	r, e := c.Files.Get(ctx, key)
	if e != nil {
		return o, "", e
	}
	defer r.Close()
	data, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if e != nil {
		return o, "", e
	}
	if len(data) > 1<<20 {
		return o, "", ErrState
	}
	// Reject duplicate fields as well as unknown/missing fields. Decode tokens
	// before typed decoding, otherwise encoding/json silently accepts duplicates.
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, e := decoder.Token()
	if e != nil || token != json.Delim('{') {
		return o, "", ErrState
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, e = decoder.Token()
		key, ok := token.(string)
		if e != nil || !ok || seen[key] {
			return o, "", ErrState
		}
		seen[key] = true
		switch key {
		case "operation_format", "operation_version", "operation_id", "backup_id", "plan_digest", "generation", "phase", "created_at", "updated_at", "heartbeat_at", "finished_at", "failure_code", "cancel_requested", "owner_id":
		default:
			return o, "", ErrState
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return o, "", ErrState
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && key != "finished_at" && key != "failure_code" {
			return o, "", ErrState
		}
		switch key {
		case "created_at", "updated_at", "heartbeat_at", "finished_at":
			if string(value) != "null" {
				var stamp string
				if json.Unmarshal(value, &stamp) != nil || len(stamp) == 0 || stamp[len(stamp)-1] != 'Z' {
					return o, "", ErrState
				}
			}
		}
	}
	if _, e = decoder.Token(); e != nil {
		return o, "", ErrState
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&o) != nil || decoder.Decode(new(any)) != io.EOF || len(seen) != 14 || o.ID != id || o.Validate() != nil {
		return Operation{}, "", ErrState
	}
	sum := sha256.Sum256(data)
	return o, hex.EncodeToString(sum[:]), nil
}

func (c Control) Update(ctx context.Context, id string, expected int64, next Operation, confirmedDead bool) error {
	old, token, e := c.Load(ctx, id)
	if e != nil {
		return e
	}
	if old.Generation != expected {
		return storage.ErrConflict
	}
	if e = ValidateTransition(old, next, expected, confirmedDead); e != nil {
		return e
	}
	data, e := json.Marshal(next)
	if e != nil {
		return ErrState
	}
	key, _ := operationKey(id)
	return c.Files.CompareAndSwap(ctx, key, token, data)
}

func (o Operation) Stale(now time.Time) bool {
	return !terminal(o.Phase) && now.Sub(o.HeartbeatAt) > 60*time.Second
}
