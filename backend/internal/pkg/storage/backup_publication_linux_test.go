//go:build linux

package storage

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const publicationOperation = "40000000-0000-4000-8000-000000000001"

func TestPublicationPreflightDoesNotLatchCommit(t *testing.T) {
	root, files := publicationFiles(t)
	tree, e := files.(PublicationDirectories).NewPublication(context.Background(), publicationOperation)
	if e != nil {
		t.Fatal(e)
	}
	defer tree.Close()
	if e = os.Chmod(root, 0750); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(root, 0700)
	for i := 0; i < 2; i++ {
		if e = tree.Commit(context.Background()); !errors.Is(e, ErrUnsafeStorage) {
			t.Fatal("preflight error hidden by latch", e)
		}
	}
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e = tree.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func publicationFiles(t *testing.T) (string, BackupFiles) {
	t.Helper()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	f, e := OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return root, f
}

func TestPublicationProcessDeath(t *testing.T) {
	if root := os.Getenv("YLREADER_PUBLICATION_HELPER_ROOT"); root != "" {
		files, e := OpenBackupFiles(root)
		if e != nil {
			t.Fatal(e)
		}
		defer files.Close()
		tree, e := files.(PublicationDirectories).NewPublication(context.Background(), publicationOperation)
		if e != nil {
			t.Fatal(e)
		}
		defer tree.Close()
		if e = tree.CreateOnly(context.Background(), "local/book/0.txt", strings.NewReader("keep"), 4); e != nil {
			t.Fatal(e)
		}
		os.Stdout.WriteString("STAGED\n")
		var signal [1]byte
		os.Stdin.Read(signal[:])
		return
	}
	root, files := publicationFiles(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPublicationProcessDeath$")
	command.Env = append(os.Environ(), "YLREADER_PUBLICATION_HELPER_ROOT="+root)
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
	if line, e := bufio.NewReader(stdout).ReadString('\n'); e != nil || line != "STAGED\n" {
		command.Wait()
		t.Fatal("helper did not stage", e)
	}
	command.Process.Kill()
	command.Wait()
	if _, e = os.Stat(filepath.Join(root, "objects-"+publicationOperation)); !os.IsNotExist(e) {
		t.Fatal("partial tree published after death", e)
	}
	pending := filepath.Join(root, ".publish-"+strings.ReplaceAll(publicationOperation, "-", ""), "local/book/0.txt")
	if body, e := os.ReadFile(pending); e != nil || string(body) != "keep" {
		t.Fatal("partial data deleted", e)
	}
	if _, e = files.(PublicationDirectories).NewPublication(ctx, publicationOperation); !errors.Is(e, ErrConflict) {
		t.Fatal("dead writer blindly resumed", e)
	}
}

func TestPublicationTreeCommitAndNoOverwrite(t *testing.T) {
	root, files := publicationFiles(t)
	ctx := context.Background()
	tree, e := files.(PublicationDirectories).NewPublication(ctx, publicationOperation)
	if e != nil {
		t.Fatal(e)
	}
	defer tree.Close()
	if e = tree.CreateOnly(ctx, "local/book/0.txt", strings.NewReader("正文"), 6); e != nil {
		t.Fatal(e)
	}
	if e = tree.CreateOnly(ctx, "local/book/0.txt", strings.NewReader("other"), 5); !errors.Is(e, ErrConflict) {
		t.Fatal("overwritten", e)
	}
	if _, e = os.Stat(filepath.Join(root, "objects-"+publicationOperation)); !os.IsNotExist(e) {
		t.Fatal("final name visible early", e)
	}
	if e = tree.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	r, e := tree.Get(ctx, "local/book/0.txt")
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(r)
	r.Close()
	if e != nil || string(body) != "正文" {
		t.Fatal("wrong bytes", e)
	}
	if e = tree.CreateOnly(ctx, "late", strings.NewReader("x"), 1); !errors.Is(e, ErrConflict) {
		t.Fatal("committed tree mutated", e)
	}
	if e = tree.Commit(ctx); !errors.Is(e, ErrConflict) {
		t.Fatal("repeated commit", e)
	}
	tree.Close()
	if body, e = os.ReadFile(filepath.Join(root, "objects-"+publicationOperation, "local/book/0.txt")); e != nil || string(body) != "正文" {
		t.Fatal("close deleted committed data", e)
	}
}

func TestPublicationConflictAndUnknownRetainData(t *testing.T) {
	for _, mode := range []string{"conflict", "sync-unknown"} {
		t.Run(mode, func(t *testing.T) {
			root, files := publicationFiles(t)
			ctx := context.Background()
			tree, e := files.(PublicationDirectories).NewPublication(ctx, publicationOperation)
			if e != nil {
				t.Fatal(e)
			}
			defer tree.Close()
			if e = tree.CreateOnly(ctx, "file", strings.NewReader("keep"), 4); e != nil {
				t.Fatal(e)
			}
			final := filepath.Join(root, "objects-"+publicationOperation)
			if mode == "conflict" {
				if e = os.Mkdir(final, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(final, "marker"), []byte("original"), 0600); e != nil {
					t.Fatal(e)
				}
				if e = tree.Commit(ctx); !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
				if body, e := os.ReadFile(filepath.Join(final, "marker")); e != nil || string(body) != "original" {
					t.Fatal("existing tree changed", e)
				}
			} else {
				native := tree.(*publicationTree)
				native.syncDirectory = func(fd int) error {
					if fd == native.parentFD {
						return errors.New("synthetic directory sync failure")
					}
					return nil
				}
				if e = tree.Commit(ctx); !errors.Is(e, ErrCommitUnknown) {
					t.Fatal(e)
				}
				if body, e := os.ReadFile(filepath.Join(final, "file")); e != nil || string(body) != "keep" {
					t.Fatal("unknown commit removed data", e)
				}
			}
			if e = tree.Commit(ctx); !errors.Is(e, ErrConflict) {
				t.Fatal("uncertain tree retried", e)
			}
		})
	}
}

func TestPublicationRefusesParentLinks(t *testing.T) {
	root, files := publicationFiles(t)
	ctx := context.Background()
	tree, e := files.(PublicationDirectories).NewPublication(ctx, publicationOperation)
	if e != nil {
		t.Fatal(e)
	}
	defer tree.Close()
	outside := t.TempDir()
	if e = os.Symlink(outside, filepath.Join(root, ".publish-"+strings.ReplaceAll(publicationOperation, "-", ""), "escape")); e != nil {
		t.Fatal(e)
	}
	if e = tree.CreateOnly(ctx, "escape/object", strings.NewReader("x"), 1); !errors.Is(e, ErrUnsafeStorage) {
		t.Fatal("followed parent link", e)
	}
	if _, e = os.Stat(filepath.Join(outside, "object")); !os.IsNotExist(e) {
		t.Fatal("escaped root", e)
	}
	if _, e = files.(PublicationDirectories).NewPublication(ctx, publicationOperation); !errors.Is(e, ErrConflict) {
		t.Fatal("reused pending tree", e)
	}
}
