package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"

	"yreader/backend/internal/pkg/storage"
)

var ErrLocations = errors.New("backup: overlapping, unavailable or changed storage locations")

type LocationSnapshot struct{ Target, Control, Temporary string }
type Locations struct {
	mu     sync.RWMutex
	files  [5]storage.BackupFiles
	config Config
}

// OpenLocations creates nothing. Missing target namespaces must be reserved
// explicitly first; every opened namespace must pass the secure driver checks.
func OpenLocations(ctx context.Context, c Config) (*Locations, error) {
	return openLocations(ctx, c, true)
}

func openLocations(ctx context.Context, c Config, requireEmpty bool) (*Locations, error) {
	if c.Validate() != nil {
		return nil, ErrConfig
	}
	result := &Locations{config: c}
	stores := []LocalStorage{c.Source.Storage, c.Target.Storage, c.Backup, c.Control}
	for i, s := range stores {
		base, e := storage.OpenBackupFiles(s.Root)
		if e != nil {
			result.Close()
			return nil, e
		}
		child, e := base.OpenDirectory(ctx, s.Namespace, false)
		base.Close()
		if e != nil {
			result.Close()
			return nil, e
		}
		result.files[i] = child
	}
	temp, e := storage.OpenBackupFiles(c.Temporary.Root)
	if e != nil {
		result.Close()
		return nil, e
	}
	result.files[4] = temp
	if _, e = result.snapshot(ctx, requireEmpty); e != nil {
		result.Close()
		return nil, e
	}
	return result, nil
}
func (l *Locations) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result error
	for i, f := range l.files {
		if f != nil {
			result = errors.Join(result, f.Close())
			l.files[i] = nil
		}
	}
	return result
}

func disjoint(locations []storage.DirectoryLocation) error {
	for _, l := range locations {
		if len(l.Ancestors) == 0 || l.Ancestors[0] != l.Identity {
			return ErrLocations
		}
	}
	for i, a := range locations {
		for _, b := range locations[i+1:] {
			// Different mounts of the same device might be bind aliases. Refuse even
			// apparently disjoint paths rather than claim that ancestry proves safety.
			if a.Identity.Device == b.Identity.Device && a.Identity.MountID != b.Identity.MountID {
				return ErrLocations
			}
			for _, ancestor := range a.Ancestors {
				if ancestor == b.Identity {
					return ErrLocations
				}
			}
			for _, ancestor := range b.Ancestors {
				if ancestor == a.Identity {
					return ErrLocations
				}
			}
		}
	}
	return nil
}
func fingerprint(info storage.DirectoryLocation, namespace string) string {
	hash := sha256.New()
	hash.Write([]byte("ylreader-localfs-location-v1\x00"))
	for _, n := range []uint64{info.Identity.Device, info.Identity.Inode, info.Identity.MountID, uint64(len(namespace))} {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], n)
		hash.Write(b[:])
	}
	hash.Write([]byte(namespace))
	return hex.EncodeToString(hash.Sum(nil))
}
func (l *Locations) Snapshot(ctx context.Context) (LocationSnapshot, error) {
	return l.snapshot(ctx, true)
}

func (l *Locations) snapshot(ctx context.Context, requireEmpty bool) (LocationSnapshot, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var infos []storage.DirectoryLocation
	for _, f := range l.files {
		if f == nil {
			return LocationSnapshot{}, ErrLocations
		}
		info, e := f.Location(ctx)
		if e != nil {
			return LocationSnapshot{}, e
		}
		infos = append(infos, info)
	}
	if e := disjoint(infos); e != nil {
		return LocationSnapshot{}, e
	}
	if requireEmpty {
		empty, e := l.files[1].Empty(ctx)
		if e != nil {
			return LocationSnapshot{}, e
		}
		if !empty {
			return LocationSnapshot{}, ErrLocations
		}
	}
	return LocationSnapshot{fingerprint(infos[1], l.config.Target.Storage.Namespace), fingerprint(infos[3], l.config.Control.Namespace), fingerprint(infos[4], "")}, nil
}

// ValidateConfigFile is only a secure file loader, not a restoration permit.
func LoadConfigFile(ctx context.Context, path string) (Config, error) {
	if !safeAbsolute(path) {
		return Config{}, ErrConfig
	}
	data, e := storage.ReadBackupSecret(ctx, filepath.Clean(path), 1<<20)
	if e != nil {
		return Config{}, e
	}
	return LoadConfig(bytes.NewReader(data))
}
