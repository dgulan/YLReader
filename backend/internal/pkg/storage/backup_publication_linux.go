//go:build linux

package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type publicationTree struct {
	*backupLocal
	publicationMu   sync.Mutex
	parentFD        int
	pending, output string
	identity        unix.Stat_t
	committing      bool
}

func (s *backupLocal) NewPublication(ctx context.Context, operation string) (PublicationTree, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if len(operation) != 36 {
		return nil, ErrInvalidKey
	}
	clean := strings.ReplaceAll(operation, "-", "")
	raw, e := hex.DecodeString(clean)
	if operation[8] != '-' || operation[13] != '-' || operation[18] != '-' || operation[23] != '-' || e != nil || len(raw) != 16 || hex.EncodeToString(raw) != clean {
		return nil, ErrInvalidKey
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.fd < 0 {
		return nil, ErrUnsafeStorage
	}
	if e = privateFD(s.fd, true); e != nil {
		return nil, e
	}
	pending, output := ".publish-"+clean, "objects-"+operation
	if e = unix.Mkdirat(s.fd, pending, 0700); e != nil {
		if errors.Is(e, unix.EEXIST) {
			return nil, ErrConflict
		}
		return nil, ErrUnsafeStorage
	}
	// Retain on every error after mkdir; never guess what is safe to delete.
	if s.syncDir(s.fd) != nil {
		return nil, ErrCommitUnknown
	}
	fd, e := unix.Openat(s.fd, pending, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, ErrUnsafeStorage
	}
	if e = privateFD(fd, true); e != nil {
		unix.Close(fd)
		return nil, e
	}
	parent, e := unix.Openat(s.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		unix.Close(fd)
		return nil, ErrUnsafeStorage
	}
	tree := &publicationTree{backupLocal: &backupLocal{fd: fd, syncDirectory: s.syncDirectory}, parentFD: parent, pending: pending, output: output}
	if unix.Fstat(fd, &tree.identity) != nil {
		tree.Close()
		return nil, ErrUnsafeStorage
	}
	return tree, nil
}

func (t *publicationTree) CreateOnly(ctx context.Context, key string, r io.Reader, size int64) error {
	if ValidatePublicationKey(key) != nil {
		return ErrInvalidKey
	}
	t.publicationMu.Lock()
	defer t.publicationMu.Unlock()
	if t.committing {
		return ErrConflict
	}
	return t.backupLocal.CreateOnly(ctx, key, r, size)
}

func (t *publicationTree) Commit(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	t.publicationMu.Lock()
	defer t.publicationMu.Unlock()
	if t.committing {
		return ErrConflict
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.fd < 0 || t.parentFD < 0 {
		return ErrUnsafeStorage
	}
	if e := privateFD(t.fd, true); e != nil {
		return e
	}
	if e := privateFD(t.parentFD, true); e != nil {
		return e
	}
	var current unix.Stat_t
	if unix.Fstatat(t.parentFD, t.pending, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != t.identity.Dev || current.Ino != t.identity.Ino || current.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrUnsafeStorage
	}
	t.committing = true
	if t.syncDir(t.fd) != nil {
		return ErrCommitUnknown
	}
	if e := unix.Renameat2(t.parentFD, t.pending, t.parentFD, t.output, unix.RENAME_NOREPLACE); e != nil {
		if errors.Is(e, unix.EEXIST) || errors.Is(e, unix.ENOTEMPTY) {
			return ErrConflict
		}
		if errors.Is(e, unix.ENOSYS) || errors.Is(e, unix.EINVAL) || errors.Is(e, unix.EOPNOTSUPP) {
			return ErrCapability
		}
		return ErrUnsafeStorage
	}
	if t.syncDir(t.parentFD) != nil {
		return ErrCommitUnknown
	}
	return nil
}

func (t *publicationTree) Close() error {
	t.publicationMu.Lock()
	defer t.publicationMu.Unlock()
	e := t.backupLocal.Close()
	if t.parentFD >= 0 {
		if unix.Close(t.parentFD) != nil {
			e = ErrUnsafeStorage
		}
		t.parentFD = -1
	}
	return e
}
