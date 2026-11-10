package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func putString(t *testing.T, fs Provider, key, content string) error {
	t.Helper()
	return fs.Put(context.Background(), key, bytes.NewReader([]byte(content)), int64(len(content)))
}

func TestLocalFSKeySafety(t *testing.T) {
	fs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	cases := []struct {
		name string
		key  string
	}{
		{"parent traversal", "../../etc/passwd"},
		{"absolute path", "/abs/path"},
		{"backslash absolute", `\abs\path`},
		{"nul byte", "a\x00b"},
		{"dot", "."},
		{"empty", ""},
	}
	for _, c := range cases {
		if err := putString(t, fs, c.key, "x"); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("%s: Put(%q) err = %v, want ErrInvalidKey", c.name, c.key, err)
		}
	}
	if err := fs.DeletePrefix(context.Background(), "../"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("DeletePrefix(../) err = %v, want ErrInvalidKey", err)
	}
	// 文件名中的 .. 不是路径段，必须允许。
	if err := putString(t, fs, "a..b.txt", "ok"); err != nil {
		t.Fatalf("Put(a..b.txt) err = %v, want nil", err)
	}
	if err := putString(t, fs, "local/x/0.txt", "chapter"); err != nil {
		t.Fatalf("Put(local/x/0.txt) err = %v", err)
	}
	rc, err := fs.Get(context.Background(), "local/x/0.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "chapter" {
		t.Fatalf("Get content = %q", data)
	}
	if err := fs.DeletePrefix(context.Background(), "local/x/"); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}
	if ok, _ := fs.Exists(context.Background(), "local/x/0.txt"); ok {
		t.Fatalf("file still exists after DeletePrefix")
	}
}

func TestLocalFSPutIsAtomicAndOverwrites(t *testing.T) {
	root := t.TempDir()
	fs, err := NewLocalFS(root)
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	if err := putString(t, fs, "k.txt", "first"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := putString(t, fs, "k.txt", "second"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	rc, err := fs.Get(context.Background(), "k.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "second" {
		t.Fatalf("content after overwrite = %q", data)
	}
	if ok, err := fs.Exists(context.Background(), "missing.txt"); err != nil || ok {
		t.Fatalf("Exists(missing) = %v, %v", ok, err)
	}
	if _, err := fs.Get(context.Background(), "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
}
