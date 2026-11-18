package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
)

var (
	ErrHeaderKeyring   = errors.New("backup: invalid or unavailable private header keyring")
	ErrHeaderEnvelope  = errors.New("backup: invalid header envelope or plaintext")
	ErrHeaderDecrypt   = errors.New("backup: header authentication failed")
	ErrHeaderInventory = errors.New("backup: header key inventory differs from manifest")
)

func strictHeaderObject(data []byte, limit int) (map[string]json.RawMessage, error) {
	if len(data) > limit || !utf8.Valid(data) || !pairedUnicodeEscapes(data) {
		return nil, ErrHeaderEnvelope
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil, ErrHeaderEnvelope
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, e = d.Token()
		key, ok := token.(string)
		if e != nil || !ok {
			return nil, ErrHeaderEnvelope
		}
		if _, ok = fields[key]; ok {
			return nil, ErrHeaderEnvelope
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, ErrHeaderEnvelope
		}
		fields[key] = raw
	}
	if _, e = d.Token(); e != nil || d.Decode(new(any)) != io.EOF {
		return nil, ErrHeaderEnvelope
	}
	return fields, nil
}

func parseRestoreHeaderKeyring(data []byte) (*headercrypto.Keyring, error) {
	if len(data) > 64<<10 {
		return nil, ErrHeaderKeyring
	}
	var file struct {
		Format  string            `json:"keyring_format"`
		Version int               `json:"keyring_version"`
		Current string            `json:"current_kid"`
		Keys    map[string]string `json:"keys"`
	}
	if exactJSON(data, &file) != nil || file.Format != "ylreader-header-keyring" || file.Version != 1 || !kidName.MatchString(file.Current) || len(file.Keys) < 1 || len(file.Keys) > 64 {
		return nil, ErrHeaderKeyring
	}
	fields, e := strictHeaderObject(data, 64<<10)
	if e != nil {
		return nil, ErrHeaderKeyring
	}
	if _, e = strictHeaderObject(fields["keys"], 64<<10); e != nil {
		return nil, ErrHeaderKeyring
	}
	keys := map[string][]byte{}
	for kid, encoded := range file.Keys {
		if !kidName.MatchString(kid) {
			return nil, ErrHeaderKeyring
		}
		key, e := base64.StdEncoding.Strict().DecodeString(encoded)
		if e != nil || len(key) != headercrypto.KeySize || base64.StdEncoding.EncodeToString(key) != encoded {
			return nil, ErrHeaderKeyring
		}
		keys[kid] = key
	}
	ring, e := headercrypto.NewKeyring(file.Current, keys)
	if e != nil {
		return nil, ErrHeaderKeyring
	}
	return ring, nil
}

func LoadRestoreHeaderKeyring(ctx context.Context, path string) (*headercrypto.Keyring, error) {
	if !safeAbsolute(path) {
		return nil, ErrHeaderKeyring
	}
	data, e := storage.ReadBackupSecret(ctx, path, 64<<10)
	if e != nil {
		return nil, ErrHeaderKeyring
	}
	defer clear(data)
	return parseRestoreHeaderKeyring(data)
}

type headerVerifier struct {
	ring           *headercrypto.Keyring
	expected, seen map[string]bool
}

func newHeaderVerifier(required []string, ring *headercrypto.Keyring) (*headerVerifier, error) {
	v := &headerVerifier{ring: ring, expected: map[string]bool{}, seen: map[string]bool{}}
	for i, kid := range required {
		if !kidName.MatchString(kid) || (i > 0 && required[i-1] >= kid) {
			return nil, ErrHeaderInventory
		}
		if !ring.HasKID(kid) {
			return nil, ErrHeaderKey
		}
		v.expected[kid] = true
	}
	return v, nil
}

func (v *headerVerifier) check(user, source uuid.UUID, raw []byte) error {
	fields, e := strictHeaderObject(raw, 128<<10)
	if e != nil {
		return ErrHeaderEnvelope
	}
	if len(fields) == 0 {
		return nil
	}
	var envelope string
	if len(fields) != 1 || json.Unmarshal(fields[headercrypto.EnvelopeKey], &envelope) != nil {
		return ErrHeaderEnvelope
	}
	parts := strings.SplitN(envelope, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" || !kidName.MatchString(parts[1]) || parts[2] == "" {
		return ErrHeaderEnvelope
	}
	if !v.expected[parts[1]] {
		return ErrHeaderInventory
	}
	plain, e := v.ring.Open(envelope, headercrypto.AAD(user, source))
	if e != nil {
		return ErrHeaderDecrypt
	}
	defer clear(plain)
	trimmed := bytes.TrimSpace(plain)
	if len(plain) > 64<<10 || !utf8.Valid(plain) || !pairedUnicodeEscapes(plain) || !json.Valid(plain) || len(trimmed) == 0 || trimmed[0] != '{' {
		return ErrHeaderEnvelope
	}
	v.seen[parts[1]] = true
	return nil
}

func (v *headerVerifier) finish() error {
	if len(v.seen) != len(v.expected) {
		return ErrHeaderInventory
	}
	return nil
}

func validateRestoredHeaders(ctx context.Context, tx pgx.Tx, required []string, ring *headercrypto.Keyring) error {
	v, e := newHeaderVerifier(required, ring)
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT user_id,id,CASE WHEN octet_length(header::text)<=131072 THEN header::text ELSE NULL END FROM public.book_sources ORDER BY id`)
	if e != nil {
		return ErrHeaderEnvelope
	}
	defer rows.Close()
	for rows.Next() {
		if e = ctx.Err(); e != nil {
			return e
		}
		var user, source uuid.UUID
		var raw *string
		if rows.Scan(&user, &source, &raw) != nil || raw == nil {
			return ErrHeaderEnvelope
		}
		if e = v.check(user, source, []byte(*raw)); e != nil {
			return e
		}
	}
	if rows.Err() != nil {
		return ErrHeaderEnvelope
	}
	return v.finish()
}

// Explicit checked path; no global/environment key fallback and no publication.
func (g *restoreGuard) RunDatabaseWithHeaders(ctx context.Context, stage *stagedObjects, ownerID, confirmed string, now time.Time, tool PostgresArchiveTool, budget int64, ring *headercrypto.Keyring) (recordedDatabaseResult, error) {
	if _, e := newHeaderVerifier(g.verified.manifest.HeaderKIDs, ring); e != nil {
		return recordedDatabaseResult{}, e
	}
	checked := false
	result, e := g.runDatabaseRecorded(ctx, stage, ownerID, confirmed, now, tool, budget, PostgresTargetInspector{}, func(ctx context.Context, raw string, pkg *VerifiedPackage, tool PostgresArchiveTool, budget int64, objects *stagedObjects) (databaseRestoreResult, error) {
		return restoreDatabaseWithGate(ctx, raw, pkg, tool, budget, objects, func(ctx context.Context, tx pgx.Tx) error {
			e := validateRestoredHeaders(ctx, tx, pkg.manifest.HeaderKIDs, ring)
			checked = e == nil
			return e
		})
	})
	result.HeadersChecked = e == nil && result.Executed && result.Database != nil && checked && result.Attempt.DatabaseOutcome == "committed"
	if result.HeadersChecked {
		g.mu.Lock()
		if g.locations != nil {
			g.headerProof = &headerPublicationProof{binding: g.packageBinding, owner: ownerID, stage: stage, ring: ring}
		}
		g.mu.Unlock()
	}
	return result, e
}
