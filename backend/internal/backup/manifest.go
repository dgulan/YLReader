package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"

	"yreader/backend/internal/migration"
)

var (
	ErrPackageIntegrity = errors.New("backup: package integrity verification failed")
	ErrPackageSchema    = errors.New("backup: unsupported or invalid package manifest")
	ErrPackageLimit     = errors.New("backup: package resource limit exceeded")
	ErrHeaderKey        = errors.New("backup: required header key unavailable")
)
var objectID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var revisionID = regexp.MustCompile(`^[a-f0-9]{40}$`)
var kidName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type ManifestMigration struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}
type ArchiveFile struct {
	Name  string `json:"archive_name"`
	Bytes int64  `json:"bytes"`
	SHA   string `json:"sha256"`
}
type ManifestObject struct {
	ID    string `json:"opaque_id"`
	Key   string `json:"key"`
	Bytes int64  `json:"bytes"`
	SHA   string `json:"sha256"`
}
type ManifestCounts struct {
	Migrations int64 `json:"schema_migrations"`
	Users      int64 `json:"users"`
	Sessions   int64 `json:"auth_sessions"`
	Settings   int64 `json:"app_settings"`
	Sources    int64 `json:"book_sources"`
	Failures   int64 `json:"source_failures"`
	Books      int64 `json:"books"`
	Mappings   int64 `json:"book_source_mappings"`
	Chapters   int64 `json:"chapters"`
	Progress   int64 `json:"reading_progress"`
	Overrides  int64 `json:"chapter_source_overrides"`
	Bookmarks  int64 `json:"bookmarks"`
	Rules      int64 `json:"replace_rules"`
	Jobs       int64 `json:"jobs"`
}
type Manifest struct {
	Format        string              `json:"format"`
	Version       int                 `json:"format_version"`
	BackupID      string              `json:"backup_id"`
	SourceID      string              `json:"source_instance_id"`
	Created       string              `json:"created_at"`
	Snapshot      string              `json:"snapshot_at"`
	Revision      string              `json:"app_revision"`
	SchemaVersion int                 `json:"schema_version"`
	Migrations    []ManifestMigration `json:"migrations"`
	PostgresMajor int                 `json:"postgres_major"`
	HeaderKIDs    []string            `json:"required_header_kids"`
	Database      ArchiveFile         `json:"database"`
	Objects       []ManifestObject    `json:"objects"`
	Counts        ManifestCounts      `json:"counts"`
	ObjectCount   int64               `json:"object_count"`
	TotalBytes    int64               `json:"total_plaintext_bytes"`
}

// parseManifest decodes one object entry at a time, without buffering the full
// JSON document. Uniqueness/order metadata remains O(number of objects).
func parseManifest(ctx context.Context, reader io.Reader, limits Limits) (result Manifest, err error) {
	if limits.Validate() != nil {
		return result, ErrPackageLimit
	}
	guard := &manifestReader{reader: packageContextReader{ctx, reader}, documentLimit: limits.Manifest}
	defer func() {
		if err != nil && guard.exceeded {
			err = ErrPackageLimit
		}
	}()
	decoder := json.NewDecoder(guard)
	token, e := decoder.Token()
	if e != nil || token != json.Delim('{') {
		return result, ErrPackageSchema
	}
	value := reflect.ValueOf(&result).Elem()
	typ := value.Type()
	fields := map[string]int{}
	for i := 0; i < typ.NumField(); i++ {
		fields[typ.Field(i).Tag.Get("json")] = i
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	keys := map[string]bool{}
	for decoder.More() {
		if e = ctx.Err(); e != nil {
			return Manifest{}, e
		}
		token, e = decoder.Token()
		key, ok := token.(string)
		index, known := fields[key]
		if e != nil || !ok || !known || seen[key] {
			return Manifest{}, ErrPackageSchema
		}
		seen[key] = true
		if key == "objects" {
			token, e = decoder.Token()
			if e != nil || token != json.Delim('[') {
				return Manifest{}, ErrPackageSchema
			}
			for decoder.More() {
				if e = ctx.Err(); e != nil {
					return Manifest{}, e
				}
				if int64(len(result.Objects)) >= limits.Objects {
					return Manifest{}, ErrPackageLimit
				}
				var raw json.RawMessage
				if decoder.Decode(&raw) != nil {
					return Manifest{}, ErrPackageSchema
				}
				if len(raw) > 16<<10 {
					return Manifest{}, ErrPackageLimit
				}
				var object ManifestObject
				if exactJSON(raw, &object) != nil || !objectID.MatchString(object.ID) || !safeKey(object.Key) || !validSHA(object.SHA) || object.Bytes < 0 {
					return Manifest{}, ErrPackageSchema
				}
				if object.Bytes > limits.Object || object.Bytes > 10<<30 {
					return Manifest{}, ErrPackageLimit
				}
				if ids[object.ID] || keys[object.Key] {
					return Manifest{}, ErrPackageSchema
				}
				ids[object.ID] = true
				keys[object.Key] = true
				result.Objects = append(result.Objects, object)
			}
			if _, e = decoder.Token(); e != nil {
				return Manifest{}, ErrPackageSchema
			}
		} else {
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil {
				return Manifest{}, ErrPackageSchema
			}
			if len(raw) > 1<<20 {
				return Manifest{}, ErrPackageLimit
			}
			if exactJSON(raw, value.Field(index).Addr().Interface()) != nil {
				return Manifest{}, ErrPackageSchema
			}
		}
	}
	if _, e = decoder.Token(); e != nil || decoder.Decode(new(any)) != io.EOF || len(seen) != typ.NumField() {
		return Manifest{}, ErrPackageSchema
	}
	if result.Format != "ylreader-instance-backup" || result.Version != 1 || !validUUID(result.BackupID) || !validUUID(result.SourceID) || !revisionID.MatchString(result.Revision) || result.SchemaVersion != 1 || result.PostgresMajor != 16 {
		return Manifest{}, ErrPackageSchema
	}
	created, e1 := time.Parse(time.RFC3339Nano, result.Created)
	snapshot, e2 := time.Parse(time.RFC3339Nano, result.Snapshot)
	if e1 != nil || e2 != nil || !utcText.MatchString(result.Created) || !utcText.MatchString(result.Snapshot) || snapshot.After(created) {
		return Manifest{}, ErrPackageSchema
	}
	expected := migration.Expected()
	if len(result.Migrations) != 1 || result.Migrations[0] != (ManifestMigration{1, expected.Name, expected.Checksum}) {
		return Manifest{}, ErrPackageSchema
	}
	if result.Database.Name != "database.dump" || result.Database.Bytes < 0 || result.Database.Bytes > 200<<30 || !validSHA(result.Database.SHA) || result.ObjectCount != int64(len(result.Objects)) || len(result.HeaderKIDs) > 1024 {
		return Manifest{}, ErrPackageSchema
	}
	total := result.Database.Bytes
	for _, object := range result.Objects {
		if object.Bytes > limits.Plaintext-total {
			return Manifest{}, ErrPackageLimit
		}
		total += object.Bytes
	}
	if total > limits.Plaintext || result.TotalBytes > limits.Plaintext {
		return Manifest{}, ErrPackageLimit
	}
	if total != result.TotalBytes {
		return Manifest{}, ErrPackageSchema
	}
	counts := reflect.ValueOf(result.Counts)
	for i := 0; i < counts.NumField(); i++ {
		if n := counts.Field(i).Int(); n < 0 || n > 9007199254740991 {
			return Manifest{}, ErrPackageSchema
		}
	}
	for i, kid := range result.HeaderKIDs {
		if !kidName.MatchString(kid) || i > 0 && result.HeaderKIDs[i-1] >= kid {
			return Manifest{}, ErrPackageSchema
		}
	}
	sort.Slice(result.Objects, func(i, j int) bool { return result.Objects[i].ID < result.Objects[j].ID })
	return result, nil
}

func manifestUTF8Hash(ctx context.Context, r io.Reader) (string, error) {
	buffer := make([]byte, 32768+utf8.UTFMax)
	pending := 0
	hash := sha256.New()
	for {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		n, e := r.Read(buffer[pending:32768])
		hash.Write(buffer[pending : pending+n])
		total := pending + n
		at := 0
		for at < total {
			if !utf8.FullRune(buffer[at:total]) {
				break
			}
			value, size := utf8.DecodeRune(buffer[at:total])
			if value == utf8.RuneError && size == 1 {
				return "", ErrPackageSchema
			}
			at += size
		}
		pending = copy(buffer, buffer[at:total])
		if e == io.EOF {
			if pending != 0 {
				return "", ErrPackageSchema
			}
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if e != nil {
			return "", ErrPackageIntegrity
		}
	}
}
