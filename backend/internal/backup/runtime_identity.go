package backup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	headercrypto "yreader/backend/internal/pkg/crypto"
)

var errRuntimeIdentity = errors.New("backup: runtime configuration unavailable or changed")

type runtimeConfigBinding struct {
	TargetID        string `json:"target_instance_id"`
	ConfigRevision  string `json:"config_revision_id"`
	JWTRevision     string `json:"jwt_revision_id"`
	KeyringRevision string `json:"keyring_revision_id"`
	CurrentKID      string `json:"current_header_kid"`
}

func (r runtimeConfigBinding) valid() bool {
	return validUUID(r.TargetID) && validUUID(r.ConfigRevision) && validUUID(r.JWTRevision) && validUUID(r.KeyringRevision) && kidName.MatchString(r.CurrentKID)
}

// This validates explicit assembly, not the configuration of a running server.
// Caller-owned secrets are only compared/used in memory, never hashed or logged.
func probeHandoverRuntime(expected, actual runtimeConfigBinding, sourceJWT, targetJWT []byte, ring *headercrypto.Keyring, required []string) (string, error) {
	if !expected.valid() || expected != actual || len(sourceJWT) < 32 || len(sourceJWT) > 4096 || len(targetJWT) < 32 || len(targetJWT) > 4096 ||
		bytes.Equal(sourceJWT, targetJWT) || ring == nil || ring.CurrentKID() != actual.CurrentKID || len(required) > 64 {
		return "", errRuntimeIdentity
	}
	for i, kid := range required {
		if !kidName.MatchString(kid) || (i > 0 && required[i-1] >= kid) || !ring.HasKID(kid) {
			return "", errRuntimeIdentity
		}
	}
	challenge := make([]byte, 32)
	defer clear(challenge)
	if _, e := rand.Read(challenge); e != nil {
		return "", errRuntimeIdentity
	}
	envelope, e := ring.Seal(challenge, "ylreader-handover-runtime-v1")
	if e != nil {
		return "", errRuntimeIdentity
	}
	plain, e := ring.Open(envelope, "ylreader-handover-runtime-v1")
	defer clear(plain)
	if e != nil || !bytes.Equal(plain, challenge) {
		return "", errRuntimeIdentity
	}
	// Normalize the empty required inventory to [], not null.
	kids := append([]string{}, required...)
	data, e := json.Marshal(struct {
		Format   string               `json:"format"`
		Version  int                  `json:"version"`
		Binding  runtimeConfigBinding `json:"binding"`
		Required []string             `json:"required_header_kids"`
	}{"ylreader-runtime-config-revision", 1, actual, kids})
	if e != nil {
		return "", errRuntimeIdentity
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
