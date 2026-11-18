//go:build linux

package storage

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sys/unix"
)

type restoreLease struct {
	mu       sync.Mutex
	fd       int
	identity unix.Stat_t
}

func (s *backupLocal) TryRestoreLock(ctx context.Context) (RestoreLease, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.fd < 0 {
		return nil, ErrUnsafeStorage
	}
	fd, e := unix.Openat(s.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, ErrUnsafeStorage
	}
	var fs unix.Statfs_t
	if unix.Fstatfs(fd, &fs) != nil {
		unix.Close(fd)
		return nil, ErrCapability
	}
	switch uint64(fs.Type) {
	case unix.TMPFS_MAGIC, unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.OVERLAYFS_SUPER_MAGIC:
	default:
		unix.Close(fd)
		return nil, ErrCapability
	}
	if e = privateFD(fd, true); e != nil {
		unix.Close(fd)
		return nil, e
	}
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		unix.Close(fd)
		if errors.Is(e, unix.EWOULDBLOCK) || errors.Is(e, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, ErrCapability
	}
	lease := &restoreLease{fd: fd}
	if unix.Fstat(fd, &lease.identity) != nil || lease.identity.Nlink < 2 {
		lease.Close()
		return nil, ErrUnsafeStorage
	}
	if e = ctx.Err(); e != nil {
		lease.Close()
		return nil, e
	}
	return lease, nil
}

func (l *restoreLease) Validate(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fd < 0 {
		return ErrUnsafeStorage
	}
	if e := privateFD(l.fd, true); e != nil {
		return e
	}
	var st unix.Stat_t
	if unix.Fstat(l.fd, &st) != nil || st.Nlink < 2 || st.Dev != l.identity.Dev || st.Ino != l.identity.Ino {
		return ErrUnsafeStorage
	}
	return nil
}

func (l *restoreLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fd < 0 {
		return nil
	}
	e := unix.Flock(l.fd, unix.LOCK_UN)
	closed := unix.Close(l.fd)
	l.fd = -1
	if e != nil || closed != nil {
		return ErrUnsafeStorage
	}
	return nil
}
