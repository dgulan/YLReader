package backup

import (
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrConfig = errors.New("backup: invalid experiment configuration")
var utcText = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?Z$`)
var absoluteFile = regexp.MustCompile(`^([A-Za-z]:[\\/]|/)`)

type LocalStorage struct {
	Provider  string `json:"provider"`
	Root      string `json:"root"`
	Namespace string `json:"namespace"`
}
type Endpoint struct {
	InstanceID string       `json:"instance_id"`
	DSNFile    string       `json:"database_dsn_file"`
	Storage    LocalStorage `json:"storage"`
}
type Limits struct {
	Ciphertext int64 `json:"max_ciphertext_bytes"`
	Plaintext  int64 `json:"max_plaintext_bytes"`
	Manifest   int64 `json:"max_manifest_bytes"`
	Objects    int64 `json:"max_objects"`
	Object     int64 `json:"max_object_bytes"`
}
type Config struct {
	Format    string       `json:"config_format"`
	Version   int          `json:"config_version"`
	Source    Endpoint     `json:"source"`
	Target    Endpoint     `json:"target"`
	Backup    LocalStorage `json:"backup"`
	Control   LocalStorage `json:"control"`
	Temporary struct {
		Root string `json:"root"`
	} `json:"temporary"`
	Encryption struct {
		Recipient string `json:"recipient_file,omitempty"`
		Identity  string `json:"identity_file,omitempty"`
	} `json:"encryption"`
	QuiescenceFile string `json:"quiescence_file"`
	Limits         Limits `json:"limits"`
}

func LoadConfig(reader io.Reader) (Config, error) {
	var c Config
	if reader == nil {
		return c, ErrConfig
	}
	data, e := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if e != nil {
		return c, ErrConfig
	}
	if exactJSON(data, &c) != nil || c.Validate() != nil {
		return Config{}, ErrConfig
	}
	return c, nil
}
func (l Limits) Validate() error {
	if l.Ciphertext < 1 || l.Ciphertext > 1<<40 || l.Plaintext < 1 || l.Plaintext > 1<<40 || l.Manifest < 1 || l.Manifest > 256<<20 || l.Objects < 0 || l.Objects > 1000000 || l.Object < 1 || l.Object > 10<<30 {
		return ErrConfig
	}
	return nil
}
func safeAbsolute(p string) bool {
	if len(p) > 4096 || !utf8.ValidString(p) || !absoluteFile.MatchString(p) || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `\\`) {
		return false
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return false
		}
	}
	for _, part := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
func safeKey(key string) bool {
	if key == "" || utf8.RuneCountInString(key) > 1024 || strings.ContainsAny(key, "\\:") || strings.HasPrefix(key, "/") {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, r := range key {
		if r < 32 || r == 127 {
			return false
		}
	}
	return utf8.ValidString(key)
}
func (s LocalStorage) Validate() error {
	if s.Provider != "localfs" || !safeAbsolute(s.Root) || !safeKey(s.Namespace) {
		return ErrConfig
	}
	return nil
}
func lexicalLocation(root, namespace string) string {
	root = strings.ReplaceAll(root, `\`, "/")
	value := path.Clean(root + "/" + namespace)
	if len(root) > 1 && root[1] == ':' {
		value = strings.ToLower(value)
	}
	return value
}
func (c Config) Validate() error {
	if c.Format != "ylreader-backup-localfs-experiment" || c.Version != 1 || !validUUID(c.Source.InstanceID) || !validUUID(c.Target.InstanceID) || c.Source.InstanceID == c.Target.InstanceID || c.Limits.Validate() != nil {
		return ErrConfig
	}
	paths := []string{c.Source.DSNFile, c.Target.DSNFile, c.QuiescenceFile, c.Temporary.Root}
	if c.Encryption.Recipient != "" {
		paths = append(paths, c.Encryption.Recipient)
	}
	if c.Encryption.Identity != "" {
		paths = append(paths, c.Encryption.Identity)
	}
	if c.Encryption.Recipient == "" && c.Encryption.Identity == "" {
		return ErrConfig
	}
	for _, p := range paths {
		if !safeAbsolute(p) {
			return ErrConfig
		}
	}
	locations := []string{lexicalLocation(c.Temporary.Root, "")}
	for _, s := range []LocalStorage{c.Source.Storage, c.Target.Storage, c.Backup, c.Control} {
		if s.Validate() != nil {
			return ErrConfig
		}
		locations = append(locations, lexicalLocation(s.Root, s.Namespace))
	}
	for i, a := range locations {
		for _, b := range locations[i+1:] {
			if a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/") {
				return ErrConfig
			}
		}
	}
	return nil
}
