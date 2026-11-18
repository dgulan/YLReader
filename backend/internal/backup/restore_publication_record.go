package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"yreader/backend/internal/pkg/storage"
)

var ErrPublication = errors.New("backup: object publication unconfirmed; retain both layouts")
var ErrPublicationState = errors.New("backup: invalid or conflicting object publication")

type publicationReceipt struct {
	Format            string `json:"publication_format"`
	Version           int    `json:"publication_version"`
	OperationID       string `json:"operation_id"`
	BackupID          string `json:"backup_id"`
	PlanDigest        string `json:"plan_digest"`
	OwnerID           string `json:"owner_id"`
	PackageSHA        string `json:"package_sha256"`
	ManifestSHA       string `json:"manifest_sha256"`
	TargetFingerprint string `json:"target_storage_fingerprint"`
	Staging           string `json:"staging_reference"`
	Pending           string `json:"pending_reference"`
	Output            string `json:"output_reference"`
	TreeFingerprint   string `json:"tree_fingerprint"`
	State             string `json:"state"`
}

func publicationFor(p Plan, owner, staging string) publicationReceipt {
	return publicationReceipt{Format: "ylreader-object-publication", Version: 1, OperationID: p.OperationID, BackupID: p.BackupID, PlanDigest: p.Digest, OwnerID: owner, PackageSHA: p.PackageSHA, ManifestSHA: p.ManifestSHA, TargetFingerprint: p.TargetFingerprint, Staging: staging, Pending: ".publish-" + strings.ReplaceAll(p.OperationID, "-", ""), Output: "objects-" + p.OperationID, State: "planned"}
}

func (p publicationReceipt) validate() error {
	if p.Format != "ylreader-object-publication" || p.Version != 1 || !validUUID(p.OperationID) || !validUUID(p.BackupID) || !validUUID(p.OwnerID) || !validSHA(p.PlanDigest) || !validSHA(p.PackageSHA) || !validSHA(p.ManifestSHA) || !validSHA(p.TargetFingerprint) || !generationReference.MatchString(p.Staging) || p.Pending != ".publish-"+strings.ReplaceAll(p.OperationID, "-", "") || p.Output != "objects-"+p.OperationID {
		return ErrPublicationState
	}
	if p.State == "planned" {
		if p.TreeFingerprint != "" {
			return ErrPublicationState
		}
		return nil
	}
	if (p.State != "copying" && p.State != "publishing" && p.State != "verified") || !validSHA(p.TreeFingerprint) {
		return ErrPublicationState
	}
	return nil
}

func publicationTransition(old, next publicationReceipt) error {
	if old.validate() != nil || next.validate() != nil {
		return ErrPublicationState
	}
	a, b := old, next
	a.State = ""
	b.State = ""
	a.TreeFingerprint = ""
	b.TreeFingerprint = ""
	if a != b {
		return ErrPublicationState
	}
	forward := map[string]string{"planned": "copying", "copying": "publishing", "publishing": "verified"}
	if forward[old.State] != next.State || (old.TreeFingerprint != "" && old.TreeFingerprint != next.TreeFingerprint) {
		return ErrPublicationState
	}
	return nil
}

type publicationStore struct{ Files storage.ConditionalFiles }

func publicationKey(id string) (string, error) {
	if !validUUID(id) {
		return "", ErrPublicationState
	}
	return "operations/" + id + "/publication.json", nil
}
func (s publicationStore) Create(ctx context.Context, p publicationReceipt) error {
	if s.Files == nil || p.validate() != nil || p.State != "planned" {
		return ErrPublicationState
	}
	key, _ := publicationKey(p.OperationID)
	data, _ := json.Marshal(p)
	return s.Files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data)))
}
func (s publicationStore) Load(ctx context.Context, id string) (publicationReceipt, string, error) {
	key, e := publicationKey(id)
	if e != nil || s.Files == nil {
		return publicationReceipt{}, "", ErrPublicationState
	}
	r, e := s.Files.Get(ctx, key)
	if e != nil {
		return publicationReceipt{}, "", e
	}
	data, e := io.ReadAll(io.LimitReader(r, 8193))
	closed := r.Close()
	var p publicationReceipt
	if e != nil || closed != nil || len(data) > 8192 || exactJSON(data, &p) != nil || p.validate() != nil || p.OperationID != id {
		return p, "", ErrPublicationState
	}
	h := sha256.Sum256(data)
	return p, hex.EncodeToString(h[:]), nil
}
func (s publicationStore) Update(ctx context.Context, id, token string, next publicationReceipt) error {
	old, current, e := s.Load(ctx, id)
	if e != nil {
		return e
	}
	if token != current {
		return storage.ErrConflict
	}
	if publicationTransition(old, next) != nil {
		return ErrPublicationState
	}
	key, _ := publicationKey(id)
	data, _ := json.Marshal(next)
	return s.Files.CompareAndSwap(ctx, key, token, data)
}
