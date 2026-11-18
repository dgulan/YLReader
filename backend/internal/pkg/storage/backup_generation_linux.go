//go:build linux

package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func (s *backupLocal) OnlyDirectory(ctx context.Context, name string, expected FileIdentity) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	part := strings.TrimPrefix(name, ".scratch-")
	decoded, e := hex.DecodeString(part)
	if name == part || len(decoded) != 16 || e != nil || hex.EncodeToString(decoded) != part {
		return ErrInvalidKey
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.fd < 0 {
		return ErrUnsafeStorage
	}
	if e = privateFD(s.fd, true); e != nil {
		return e
	}
	fd, e := unix.Openat(s.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return ErrUnsafeStorage
	}
	dir := os.NewFile(uintptr(fd), "restore-generation-list")
	entries, readErr := dir.ReadDir(2)
	closed := dir.Close()
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closed != nil || len(entries) != 1 || entries[0].Name() != name || !entries[0].IsDir() {
		return ErrUnsafeStorage
	}
	child, e := unix.Openat(s.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return ErrUnsafeStorage
	}
	bound := &backupLocal{fd: child}
	defer bound.Close()
	info, e := bound.Location(ctx)
	if e != nil || info.Identity != expected {
		return ErrUnsafeStorage
	}
	return nil
}
