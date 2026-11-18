//go:build linux

package storage

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func secureFixture(t *testing.T) (BackupFiles, string) {
	t.Helper()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	s, e := OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}
func readBackup(t *testing.T, s interface {
	Get(context.Context, string) (io.ReadCloser, error)
}, key string) string {
	t.Helper()
	r, e := s.Get(context.Background(), key)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	b, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func digest(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func TestBackupFilesAtomicControl(t *testing.T) {
	s, root := secureFixture(t)
	ctx := context.Background()
	key := "operations/id/state.json"
	if e := s.CreateOnly(ctx, key, strings.NewReader("old"), 3); e != nil {
		t.Fatal(e)
	}
	if e := s.CreateOnly(ctx, key, strings.NewReader("overwrite"), 9); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e := s.CompareAndSwap(ctx, key, digest("wrong"), []byte("next")); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if got := readBackup(t, s, key); got != "old" {
		t.Fatal(got)
	}
	// Two independent handles/instances must coordinate through the OS lock.
	other, e := OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writer := s
			if i%2 == 1 {
				writer = other
			}
			results <- writer.CompareAndSwap(ctx, key, digest("old"), []byte("next"))
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrBusy) {
			t.Fatal(e)
		}
	}
	if successes != 1 || readBackup(t, s, key) != "next" {
		t.Fatal("CAS did not select exactly one winner")
	}
}

func TestBackupFilesCreateOnlyConcurrent(t *testing.T) {
	s, _ := secureFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.CreateOnly(ctx, "once", strings.NewReader("complete"), 8) }()
	}
	wg.Wait()
	close(results)
	winners := 0
	for e := range results {
		if e == nil {
			winners++
		} else if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	if winners != 1 || readBackup(t, s, "once") != "complete" {
		t.Fatal("exclusive publication lost atomicity")
	}
}

func TestBackupFilesSyncFailureIsUncertain(t *testing.T) {
	files, _ := secureFixture(t)
	s := files.(*backupLocal)
	ctx := context.Background()
	if e := s.CreateOnly(ctx, "state.json", strings.NewReader("old"), 3); e != nil {
		t.Fatal(e)
	}
	s.syncDirectory = func(int) error { return unix.EIO }
	if e := s.CompareAndSwap(ctx, "state.json", digest("old"), []byte("new")); !errors.Is(e, ErrCommitUnknown) {
		t.Fatal("durability failure falsely acknowledged", e)
	}
	// Rename already happened: callers must refresh status, not blindly retry.
	if readBackup(t, s, "state.json") != "new" {
		t.Fatal("unexpected visible state")
	}
	s.syncDirectory = nil
	if e := s.CompareAndSwap(ctx, "state.json", digest("old"), []byte("retry")); !errors.Is(e, ErrConflict) {
		t.Fatal("stale retry overwrote visible state", e)
	}
}

func TestBackupLocationAndSecretReads(t *testing.T) {
	files, root := secureFixture(t)
	ctx := context.Background()
	parent, e := files.Location(ctx)
	if e != nil {
		t.Fatal(e)
	}
	child, e := files.OpenDirectory(ctx, "namespace/child", true)
	if e != nil {
		t.Fatal(e)
	}
	defer child.Close()
	location, e := child.Location(ctx)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, ancestor := range location.Ancestors {
		if ancestor == parent.Identity {
			found = true
		}
	}
	if !found || location.Identity == parent.Identity {
		t.Fatal("ancestry identity missing")
	}
	if empty, e := child.Empty(ctx); e != nil || !empty {
		t.Fatal("new child not empty", e)
	}
	if e = child.CreateOnly(ctx, "entry", strings.NewReader("x"), 1); e != nil {
		t.Fatal(e)
	}
	if empty, e := child.Empty(ctx); e != nil || empty {
		t.Fatal("nonempty child accepted", e)
	}
	path := filepath.Join(root, "synthetic.dsn")
	if e = os.WriteFile(path, []byte("synthetic"), 0600); e != nil {
		t.Fatal(e)
	}
	if data, e := ReadBackupSecret(ctx, path, 9); e != nil || string(data) != "synthetic" {
		t.Fatal("private read", e)
	}
	if _, e = ReadBackupSecret(ctx, path, 8); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("unbounded secret read", e)
	}
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadBackupSecret(ctx, path, 9); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("public secret accepted", e)
	}
}

func TestUnlinkedReadSnapshotIsConflict(t *testing.T) {
	files, root := secureFixture(t)
	s := files.(*backupLocal)
	if e := s.CreateOnly(context.Background(), "state.json", strings.NewReader("old"), 3); e != nil {
		t.Fatal(e)
	}
	fd, e := unix.Openat(s.fd, "state.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer unix.Close(fd)
	if e = os.Remove(filepath.Join(root, "state.json")); e != nil {
		t.Fatal(e)
	}
	if e = validateReadFD(fd); !errors.Is(e, ErrConflict) {
		t.Fatal("legitimate replaced-inode race misclassified", e)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestBackupFilesPartialFailure(t *testing.T) {
	s, root := secureFixture(t)
	ctx := context.Background()
	for _, r := range []io.Reader{brokenReader{}, strings.NewReader("short"), strings.NewReader("too long for declared size")} {
		if e := s.CreateOnly(ctx, "state.json", r, 8); e == nil {
			t.Fatal("partial file published")
		}
		if _, e := s.Get(ctx, "state.json"); !errors.Is(e, ErrNotFound) {
			t.Fatal(e)
		}
	}
	entries, e := os.ReadDir(root)
	if e != nil || len(entries) != 0 {
		t.Fatalf("pending debris: %v %v", entries, e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e := s.CreateOnly(canceled, "state.json", strings.NewReader("x"), 1); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestBackupFilesPermissionsAndAliases(t *testing.T) {
	s, root := secureFixture(t)
	ctx := context.Background()
	outside := t.TempDir()
	for _, key := range []string{"../escape", "/absolute", "a/../b", "a//b", "a\\b", "a\x00b", ".control.lock"} {
		if e := s.CreateOnly(ctx, key, strings.NewReader("x"), 1); !errors.Is(e, ErrInvalidKey) {
			t.Fatalf("key accepted %q: %v", key, e)
		}
	}
	if e := os.Symlink(outside, filepath.Join(root, "alias")); e != nil {
		t.Fatal(e)
	}
	if e := s.CreateOnly(ctx, "alias/x", strings.NewReader("x"), 1); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal(e)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("escaped root")
	}
	if e := s.CreateOnly(ctx, "plain", strings.NewReader("x"), 1); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(filepath.Join(root, "plain"), filepath.Join(root, "hard")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Get(ctx, "plain"); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("hard link accepted", e)
	}
	os.Remove(filepath.Join(root, "hard"))
	if e := os.Chmod(filepath.Join(root, "plain"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Get(ctx, "plain"); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("public file accepted", e)
	}
	if e := os.Chmod(root, 0755); e != nil {
		t.Fatal(e)
	}
	if opened, e := OpenBackupFiles(root); e == nil {
		opened.Close()
		t.Fatal("public root accepted")
	}
}

func TestBackupFilesPOSIXACLRejected(t *testing.T) {
	s, root := secureFixture(t)
	if e := s.CreateOnly(context.Background(), "plain", strings.NewReader("x"), 1); e != nil {
		t.Fatal(e)
	}
	// Access ACL with a named user but zero mask: mode remains 0600, so only
	// inspecting the ACL detects it. Do not grant another user plaintext access.
	acl := make([]byte, 4+5*8)
	binary.LittleEndian.PutUint32(acl, 2)
	tags := []uint16{1, 2, 4, 16, 32}
	perms := []uint16{6, 0, 0, 0, 0}
	for i, tag := range tags {
		offset := 4 + i*8
		binary.LittleEndian.PutUint16(acl[offset:], tag)
		binary.LittleEndian.PutUint16(acl[offset+2:], perms[i])
		id := uint32(0xffffffff)
		if tag == 2 {
			id = 65534
		}
		binary.LittleEndian.PutUint32(acl[offset+4:], id)
	}
	if e := unix.Setxattr(filepath.Join(root, "plain"), "system.posix_acl_access", acl, 0); e != nil {
		t.Fatalf("cannot exercise ACL rejection: %v", e)
	}
	if _, e := s.Get(context.Background(), "plain"); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("extra ACL accepted", e)
	}
}

func TestBackupFilesPinnedDirectory(t *testing.T) {
	s, root := secureFixture(t)
	parent := filepath.Dir(root)
	moved := filepath.Join(parent, "moved")
	if e := os.Rename(root, moved); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(moved) })
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e := s.CreateOnly(context.Background(), "bound", strings.NewReader("x"), 1); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(root, "bound")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("followed replacement root")
	}
	if _, e := os.Stat(filepath.Join(moved, "bound")); e != nil {
		t.Fatal(e)
	}
}

func TestBackupTempLifecycle(t *testing.T) {
	s, root := secureFixture(t)
	tmp, e := s.NewTemp(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = tmp.CreateOnly(context.Background(), "database.dump", strings.NewReader("synthetic"), 9); e != nil {
		t.Fatal(e)
	}
	if e = tmp.CreateOnly(context.Background(), "nested/plain", strings.NewReader("x"), 1); !errors.Is(e, ErrInvalidKey) {
		t.Fatal(e)
	}
	if readBackup(t, tmp, "database.dump") != "synthetic" {
		t.Fatal("temp data changed")
	}
	if e = tmp.Remove(); e != nil {
		t.Fatal(e)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("temp not removed")
	}
}

func TestBackupTempCleanupRefusesLinks(t *testing.T) {
	s, _ := secureFixture(t)
	scratch, e := s.NewTemp(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	tmp := scratch.(*backupTemp)
	outside := filepath.Join(t.TempDir(), "sentinel")
	if e = os.WriteFile(outside, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = unix.Symlinkat(outside, tmp.fd, "alias"); e != nil {
		t.Fatal(e)
	}
	if e = tmp.Remove(); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("unsafe cleanup accepted", e)
	}
	if b, e := os.ReadFile(outside); e != nil || string(b) != "keep" {
		t.Fatal("cleanup followed link")
	}
	unix.Unlinkat(tmp.fd, "alias", 0)
	if e = tmp.Remove(); e != nil {
		t.Fatal(e)
	}
}

func TestBackupFilesProcessLock(t *testing.T) {
	if root := os.Getenv("YLREADER_LOCK_HELPER"); root != "" {
		files, e := OpenBackupFiles(root)
		if e != nil {
			t.Fatal(e)
		}
		s := files.(*backupLocal)
		fd, e := unix.Openat(s.fd, ".control.lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if e = unix.Flock(fd, unix.LOCK_EX); e != nil {
			t.Fatal(e)
		}
		fmt.Println("LOCKED")
		for {
			time.Sleep(time.Second)
		}
	}
	s, root := secureFixture(t)
	ctx := context.Background()
	if e := s.CreateOnly(ctx, "state.json", strings.NewReader("old"), 3); e != nil {
		t.Fatal(e)
	}
	childCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestBackupFilesProcessLock$")
	cmd.Env = append(os.Environ(), "YLREADER_LOCK_HELPER="+root)
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(out)
	locked := false
	for scanner.Scan() {
		if scanner.Text() == "LOCKED" {
			locked = true
			break
		}
	}
	if !locked {
		t.Fatal("child never acquired lock")
	}
	if e = s.CompareAndSwap(ctx, "state.json", digest("old"), []byte("new")); !errors.Is(e, ErrBusy) {
		t.Fatal("live writer lock ignored", e)
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	cmd.Wait()
	if e = s.CompareAndSwap(ctx, "state.json", digest("old"), []byte("new")); e != nil {
		t.Fatal("dead writer lock not released", e)
	}
}
