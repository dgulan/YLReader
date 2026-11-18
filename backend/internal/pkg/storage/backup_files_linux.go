//go:build linux

package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type backupLocal struct {
	mu            sync.RWMutex
	fd            int
	syncDirectory func(int) error // nil uses fsync; tests inject publication failures.
}

func (s *backupLocal) syncDir(fd int) error {
	if s.syncDirectory != nil {
		return s.syncDirectory(fd)
	}
	return unix.Fsync(fd)
}

// OpenBackupFiles never creates/chmods the configured root or follows a link.
func OpenBackupFiles(path string) (BackupFiles, error) {
	if os.Geteuid() == 0 {
		return nil, ErrUnsafeStorage
	}
	if !filepath.IsAbs(path) {
		return nil, ErrInvalidKey
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, ErrInvalidKey
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrCapability
	}
	for _, p := range parts {
		next, e := unix.Openat(fd, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, ErrUnsafeStorage
		}
		fd = next
	}
	if err = privateFD(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &backupLocal{fd: fd}, nil
}

func privateFD(fd int, directory bool) error {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return ErrUnsafeStorage
	}
	mode := uint32(0600)
	kind := uint32(unix.S_IFREG)
	if directory {
		mode = 0700
		kind = unix.S_IFDIR
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&unix.S_IFMT != kind || st.Mode&07777 != mode || (!directory && st.Nlink != 1) {
		return ErrUnsafeStorage
	}
	for _, key := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		_, err := unix.Fgetxattr(fd, key, nil)
		if err == nil {
			return ErrUnsafeStorage
		}
		if !errors.Is(err, unix.ENODATA) {
			return ErrCapability
		}
	}
	return nil
}

func keyParts(key string) ([]string, error) {
	if key == "" || strings.ContainsAny(key, "\\:\x00") || strings.HasPrefix(key, "/") {
		return nil, ErrInvalidKey
	}
	parts := strings.Split(key, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".") {
			return nil, ErrInvalidKey
		}
		for _, r := range p {
			if r < 32 || r == 127 {
				return nil, ErrInvalidKey
			}
		}
	}
	return parts, nil
}

// parent opens each component relative to the pinned root; caller holds mu.
func (s *backupLocal) parent(key string, create bool) (int, string, error) {
	parts, err := keyParts(key)
	if err != nil {
		return -1, "", err
	}
	if s.fd < 0 {
		return -1, "", os.ErrClosed
	}
	fd, err := unix.Openat(s.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", ErrUnsafeStorage
	}
	for _, p := range parts[:len(parts)-1] {
		if create {
			e := unix.Mkdirat(fd, p, 0700)
			if e != nil && !errors.Is(e, unix.EEXIST) {
				unix.Close(fd)
				return -1, "", e
			}
			if e == nil && s.syncDir(fd) != nil {
				unix.Close(fd)
				return -1, "", ErrCommitUnknown
			}
		}
		next, e := unix.Openat(fd, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			if errors.Is(e, unix.ENOENT) {
				return -1, "", ErrNotFound
			}
			return -1, "", ErrUnsafeStorage
		}
		fd = next
		if e = privateFD(fd, true); e != nil {
			unix.Close(fd)
			return -1, "", e
		}
	}
	return fd, parts[len(parts)-1], nil
}

func readAt(fd int, name string) (*os.File, error) {
	f, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		if errors.Is(e, unix.ENOENT) {
			return nil, ErrNotFound
		}
		return nil, ErrUnsafeStorage
	}
	if e = validateReadFD(f); e != nil {
		unix.Close(f)
		return nil, e
	}
	return os.NewFile(uintptr(f), "private-object"), nil
}

func validateReadFD(fd int) error {
	e := privateFD(fd, false)
	if errors.Is(e, ErrUnsafeStorage) {
		// A reader may open the old inode immediately before an atomic CAS rename.
		// Return a retryable conflict, never relax the persistent hard-link policy.
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) == nil && stat.Nlink == 0 {
			return ErrConflict
		}
	}
	return e
}

func (s *backupLocal) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fd, name, err := s.parent(key, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return readAt(fd, name)
}

func nonce() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

type contextReader struct {
	context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.Err(); e != nil {
		return 0, e
	}
	return r.Reader.Read(p)
}

func publish(ctx context.Context, fd int, name string, r io.Reader, size int64, replace bool, syncDir func(int) error) error {
	if size < 0 || size > 1<<40 {
		return ErrInvalidKey
	}
	tmp := ".pending-" + nonce()
	n, e := unix.Openat(fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer unix.Unlinkat(fd, tmp, 0)
	f := os.NewFile(uintptr(n), "private-pending")
	defer f.Close()
	if e = privateFD(n, false); e != nil {
		return e
	}
	written, e := io.Copy(f, io.LimitReader(contextReader{ctx, r}, size+1))
	if e != nil {
		return e
	}
	if written != size {
		return io.ErrUnexpectedEOF
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if replace {
		flags = 0
	}
	if e = unix.Renameat2(fd, tmp, fd, name, flags); e != nil {
		if errors.Is(e, unix.EEXIST) {
			return ErrConflict
		}
		if errors.Is(e, unix.ENOSYS) || errors.Is(e, unix.EINVAL) || errors.Is(e, unix.EOPNOTSUPP) {
			return ErrCapability
		}
		return e
	}
	if syncDir(fd) != nil {
		return ErrCommitUnknown
	}
	return nil
}

func (s *backupLocal) CreateOnly(ctx context.Context, key string, r io.Reader, size int64) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fd, name, e := s.parent(key, true)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	return publish(ctx, fd, name, r, size, false, s.syncDir)
}

func (s *backupLocal) CompareAndSwap(ctx context.Context, key, token string, data []byte) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if len(data) > 1<<20 {
		return ErrInvalidKey
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fd, name, e := s.parent(key, false)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	lock, e := unix.Openat(fd, ".control.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return ErrUnsafeStorage
	}
	defer unix.Close(lock)
	if e = privateFD(lock, false); e != nil {
		return e
	}
	if e = unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		if errors.Is(e, unix.EWOULDBLOCK) {
			return ErrBusy
		}
		return ErrCapability
	}
	defer unix.Flock(lock, unix.LOCK_UN)
	f, e := readAt(fd, name)
	if e != nil {
		return e
	}
	old, e := io.ReadAll(io.LimitReader(contextReader{ctx, f}, (1<<20)+1))
	f.Close()
	if e != nil {
		return e
	}
	if len(old) > 1<<20 {
		return ErrUnsafeStorage
	}
	digest := sha256.Sum256(old)
	if hex.EncodeToString(digest[:]) != token {
		return ErrConflict
	}
	return publish(ctx, fd, name, strings.NewReader(string(data)), int64(len(data)), true, s.syncDir)
}

func (s *backupLocal) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 0 {
		return os.ErrClosed
	}
	e := unix.Close(s.fd)
	s.fd = -1
	return e
}

type backupTemp struct {
	*backupLocal
	parentFD int
	name     string
	identity unix.Stat_t
}

func (t *backupTemp) Reference() string { return t.name }

func (s *backupLocal) NewTemp(ctx context.Context) (BackupTemp, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.fd < 0 {
		return nil, os.ErrClosed
	}
	name := ".scratch-" + nonce()
	if e := unix.Mkdirat(s.fd, name, 0700); e != nil {
		return nil, e
	}
	if s.syncDir(s.fd) != nil {
		unix.Unlinkat(s.fd, name, unix.AT_REMOVEDIR)
		return nil, ErrCommitUnknown
	}
	fd, e := unix.Openat(s.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		unix.Unlinkat(s.fd, name, unix.AT_REMOVEDIR)
		return nil, e
	}
	if e = privateFD(fd, true); e != nil {
		unix.Close(fd)
		unix.Unlinkat(s.fd, name, unix.AT_REMOVEDIR)
		return nil, e
	}
	parent, e := unix.Openat(s.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		unix.Close(fd)
		unix.Unlinkat(s.fd, name, unix.AT_REMOVEDIR)
		return nil, e
	}
	t := &backupTemp{backupLocal: &backupLocal{fd: fd}, parentFD: parent, name: name}
	if e = unix.Fstat(fd, &t.identity); e != nil {
		unix.Close(fd)
		unix.Close(parent)
		unix.Unlinkat(s.fd, name, unix.AT_REMOVEDIR)
		return nil, e
	}
	return t, nil
}

func (t *backupTemp) CreateOnly(ctx context.Context, key string, r io.Reader, size int64) error {
	if strings.Contains(key, "/") {
		return ErrInvalidKey
	}
	return t.backupLocal.CreateOnly(ctx, key, r, size)
}

func (t *backupTemp) Remove() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fd < 0 {
		return os.ErrClosed
	}
	var current unix.Stat_t
	if unix.Fstatat(t.parentFD, t.name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != t.identity.Dev || current.Ino != t.identity.Ino {
		return ErrUnsafeStorage
	}
	fd, e := unix.Openat(t.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	dir := os.NewFile(uintptr(fd), "scratch-list")
	entries, e := dir.ReadDir(-1)
	dir.Close()
	if e != nil {
		return e
	}
	for _, entry := range entries {
		f, e := readAt(t.fd, entry.Name())
		if e != nil {
			return e
		}
		f.Close()
	}
	for _, entry := range entries {
		if e = unix.Unlinkat(t.fd, entry.Name(), 0); e != nil {
			return e
		}
	}
	if e = unix.Unlinkat(t.parentFD, t.name, unix.AT_REMOVEDIR); e != nil {
		return e
	}
	e = unix.Fsync(t.parentFD)
	unix.Close(t.fd)
	unix.Close(t.parentFD)
	t.fd = -1
	t.parentFD = -1
	if e != nil {
		return ErrCommitUnknown
	}
	return nil
}

func (t *backupTemp) Retain() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var result error
	if t.fd >= 0 {
		if unix.Close(t.fd) != nil {
			result = ErrUnsafeStorage
		}
		t.fd = -1
	}
	if t.parentFD >= 0 {
		if unix.Close(t.parentFD) != nil {
			result = ErrUnsafeStorage
		}
		t.parentFD = -1
	}
	return result
}
