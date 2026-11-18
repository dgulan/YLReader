//go:build !linux

package storage

import (
	"errors"
	"testing"
)

func TestBackupFilesUnsupportedPlatformFailsClosed(t *testing.T) {
	if files, e := OpenBackupFiles(t.TempDir()); files != nil || !errors.Is(e, ErrCapability) {
		t.Fatal("unsafe platform fallback")
	}
}
