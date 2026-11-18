//go:build linux

package storage

import (
	"context"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func fileIdentity(fd int) (FileIdentity, error) {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return FileIdentity{}, ErrUnsafeStorage
	}
	var sx unix.Statx_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_INO|unix.STATX_MNT_ID, &sx) != nil || sx.Mask&unix.STATX_MNT_ID == 0 {
		return FileIdentity{}, ErrCapability
	}
	return FileIdentity{Device: uint64(st.Dev), Inode: st.Ino, MountID: sx.Mnt_id}, nil
}

func (s *backupLocal) OpenDirectory(ctx context.Context, key string, create bool) (BackupFiles, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fd, _, e := s.parent(key+"/unused", create)
	if e != nil {
		return nil, e
	}
	return &backupLocal{fd: fd}, nil
}

func (s *backupLocal) Location(ctx context.Context) (DirectoryLocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := DirectoryLocation{}
	if e := ctx.Err(); e != nil {
		return result, e
	}
	if s.fd < 0 {
		return result, os.ErrClosed
	}
	if e := privateFD(s.fd, true); e != nil {
		return result, e
	}
	fd, e := unix.Openat(s.fd, ".", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return result, ErrCapability
	}
	defer func() { unix.Close(fd) }()
	for i := 0; i < 1024; i++ {
		if e = ctx.Err(); e != nil {
			return DirectoryLocation{}, e
		}
		current, err := fileIdentity(fd)
		if err != nil {
			return DirectoryLocation{}, err
		}
		if i == 0 {
			result.Identity = current
		}
		result.Ancestors = append(result.Ancestors, current)
		parent, err := unix.Openat(fd, "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return DirectoryLocation{}, ErrCapability
		}
		next, err := fileIdentity(parent)
		if err != nil {
			unix.Close(parent)
			return DirectoryLocation{}, err
		}
		if next == current {
			unix.Close(parent)
			return result, nil
		}
		unix.Close(fd)
		fd = parent
	}
	return DirectoryLocation{}, ErrCapability
}

func (s *backupLocal) Empty(ctx context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e := ctx.Err(); e != nil {
		return false, e
	}
	if s.fd < 0 {
		return false, os.ErrClosed
	}
	if e := privateFD(s.fd, true); e != nil {
		return false, e
	}
	fd, e := unix.Openat(s.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return false, e
	}
	directory := os.NewFile(uintptr(fd), "private-list")
	defer directory.Close()
	entries, e := directory.ReadDir(1)
	if e != nil && e != io.EOF {
		return false, e
	}
	return len(entries) == 0, nil
}

func (s *backupLocal) AvailableBytes(ctx context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	if s.fd < 0 {
		return 0, os.ErrClosed
	}
	if e := privateFD(s.fd, true); e != nil {
		return 0, e
	}
	var stat unix.Statfs_t
	if unix.Fstatfs(s.fd, &stat) != nil || stat.Bsize <= 0 {
		return 0, ErrCapability
	}
	const max = uint64(1<<63 - 1)
	size := uint64(stat.Bsize)
	if stat.Bavail > max/size {
		return int64(max), nil
	}
	return int64(stat.Bavail * size), nil
}
