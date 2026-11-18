//go:build linux

package storage

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreDirectoryLease(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	files, e := OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	lease, e := files.(RestoreLocker).TryRestoreLock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	if empty, e := files.Empty(context.Background()); e != nil || !empty {
		t.Fatal("lease created target files", e)
	}
	other, e := OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if _, e = other.(RestoreLocker).TryRestoreLock(context.Background()); !errors.Is(e, ErrBusy) {
		t.Fatal("second handle entered", e)
	}
	files.Close()
	if e = lease.Validate(context.Background()); e != nil {
		t.Fatal("lease lost with parent handle", e)
	}
	if _, e = other.(RestoreLocker).TryRestoreLock(context.Background()); !errors.Is(e, ErrBusy) {
		t.Fatal("parent close unlocked lease", e)
	}
	if e = lease.Close(); e != nil {
		t.Fatal(e)
	}
	if e = lease.Validate(context.Background()); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("closed lease validated", e)
	}
	second, e := other.(RestoreLocker).TryRestoreLock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	second.Close()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = other.(RestoreLocker).TryRestoreLock(canceled); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestRetainedTempClosesWithoutDeletion(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	files, e := OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	temp, e := files.NewTemp(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = temp.CreateOnly(context.Background(), "object", bytes.NewReader([]byte("keep")), 4); e != nil {
		t.Fatal(e)
	}
	if e = temp.(RetainedBackupTemp).Retain(); e != nil {
		t.Fatal(e)
	}
	native := temp.(*backupTemp)
	if native.fd != -1 || native.parentFD != -1 {
		t.Fatal("staging descriptors leaked")
	}
	if e = temp.(RetainedBackupTemp).Retain(); e != nil {
		t.Fatal(e)
	}
	body, e := os.ReadFile(filepath.Join(root, temp.Reference(), "object"))
	if e != nil || string(body) != "keep" {
		t.Fatal("uncertain data deleted", e)
	}
	if e = temp.Remove(); !errors.Is(e, os.ErrClosed) {
		t.Fatal("closed staging handle removed data", e)
	}
}

func TestRestoreDirectoryLeaseProcessDeath(t *testing.T) {
	if root := os.Getenv("YLREADER_LEASE_HELPER_ROOT"); root != "" {
		files, e := OpenBackupFiles(root)
		if e != nil {
			t.Fatal(e)
		}
		defer files.Close()
		lease, e := files.(RestoreLocker).TryRestoreLock(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer lease.Close()
		os.Stdout.WriteString("LOCKED\n")
		var signal [1]byte
		os.Stdin.Read(signal[:])
		return
	}
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRestoreDirectoryLeaseProcessDeath$")
	command.Env = append(os.Environ(), "YLREADER_LEASE_HELPER_ROOT="+root)
	stdout, e := command.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdin, e := command.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	defer stdin.Close()
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { command.Process.Kill() })
	if line, e := bufio.NewReader(stdout).ReadString('\n'); e != nil || line != "LOCKED\n" {
		command.Wait()
		t.Fatal("helper did not lock", e)
	}
	files, e := OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	if _, e = files.(RestoreLocker).TryRestoreLock(ctx); !errors.Is(e, ErrBusy) {
		t.Fatal("cross-process exclusion failed", e)
	}
	if e = command.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	command.Wait()
	lease, e := files.(RestoreLocker).TryRestoreLock(ctx)
	if e != nil {
		t.Fatal("dead process retained lock", e)
	}
	lease.Close()
	if empty, e := files.Empty(ctx); e != nil || !empty {
		t.Fatal("lock artifacts after process death", e)
	}
}
