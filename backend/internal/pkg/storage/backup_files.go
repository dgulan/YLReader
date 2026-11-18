package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

var (
	ErrCapability    = errors.New("storage: required safety capability unavailable")
	ErrUnsafeStorage = errors.New("storage: unsafe permissions or file identity")
	ErrConflict      = errors.New("storage: conditional write conflict")
	ErrBusy          = errors.New("storage: control lock busy")
	ErrCommitUnknown = errors.New("storage: publication durability unconfirmed")
)

// ConditionalFiles is an additional capability, not a change to Provider.
// CompareAndSwap tokens are SHA-256 of the complete previous file bytes.
type ConditionalFiles interface {
	Get(context.Context, string) (io.ReadCloser, error)
	CreateOnly(context.Context, string, io.Reader, int64) error
	CompareAndSwap(context.Context, string, string, []byte) error
}

// BackupFiles binds access to an already-private, existing directory handle.
// Unsupported platforms fail closed rather than fall back to ordinary LocalFS.
type BackupFiles interface {
	ConditionalFiles
	OpenDirectory(context.Context, string, bool) (BackupFiles, error)
	Location(context.Context) (DirectoryLocation, error)
	Empty(context.Context) (bool, error)
	AvailableBytes(context.Context) (int64, error)
	NewTemp(context.Context) (BackupTemp, error)
	Close() error
}

// FileIdentity includes mount identity so bind mounts cannot hide aliases.
type FileIdentity struct{ Device, Inode, MountID uint64 }
type DirectoryLocation struct {
	Identity  FileIdentity
	Ancestors []FileIdentity
}

// BackupTemp accepts flat names only. Remove refuses foreign aliases or links.
type BackupTemp interface {
	Reference() string
	Get(context.Context, string) (io.ReadCloser, error)
	CreateOnly(context.Context, string, io.Reader, int64) error
	Remove() error
}

// RestoreLocker is optional. A lease pins and locks a private directory inode
// without creating target objects. Unsupported filesystems fail closed.
type RestoreLocker interface {
	TryRestoreLock(context.Context) (RestoreLease, error)
}
type RestoreLease interface {
	Validate(context.Context) error
	Close() error
}

// RetainedBackupTemp closes staging handles without deleting uncertain data.
type RetainedBackupTemp interface{ Retain() error }

// RestoreDirectory validates that the sole child is the pinned generation.
type RestoreDirectory interface {
	OnlyDirectory(context.Context, string, FileIdentity) error
}

type PublicationDirectories interface {
	NewPublication(context.Context, string) (PublicationTree, error)
}

// Close retains data. No recursive cleanup/overwrite method is provided.
type PublicationTree interface {
	Get(context.Context, string) (io.ReadCloser, error)
	CreateOnly(context.Context, string, io.Reader, int64) error
	Location(context.Context) (DirectoryLocation, error)
	Commit(context.Context) error
	Close() error
}

func ValidatePublicationKey(key string) error {
	if len(key) == 0 || len(key) > 4096 || strings.ContainsAny(key, "\\:\x00") || strings.HasPrefix(key, "/") || !utf8.ValidString(key) {
		return ErrInvalidKey
	}
	parts := strings.Split(key, "/")
	if len(parts) > 64 {
		return ErrInvalidKey
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 255 || strings.HasPrefix(part, ".") {
			return ErrInvalidKey
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return ErrInvalidKey
			}
		}
	}
	return nil
}
