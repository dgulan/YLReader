package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalFS 是基于本地/挂载卷的 Provider 实现。
// 所有写操作先写同目录临时文件再 os.Rename，保证原子性并规避跨设备 EXDEV。
type LocalFS struct {
	root string
}

// NewLocalFS 创建本地存储；root 为空或无法创建时返回错误。
func NewLocalFS(root string) (*LocalFS, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("storage: empty root dir")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	return &LocalFS{root: abs}, nil
}

// Root 返回存储根目录（测试与运维用）。
func (l *LocalFS) Root() string { return l.root }

// validateKey 拒绝绝对路径、上跳与 NUL，防止越出 root。
func validateKey(key string) error {
	cleanKey := filepath.Clean(key)
	if cleanKey == "." || cleanKey == "" ||
		strings.HasPrefix(cleanKey, "/") || strings.HasPrefix(cleanKey, `\`) ||
		strings.Contains(cleanKey, "\x00") {
		return ErrInvalidKey
	}
	for _, p := range strings.Split(filepath.ToSlash(cleanKey), "/") {
		if p == ".." {
			return ErrInvalidKey
		}
	}
	return nil
}

func (l *LocalFS) path(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return filepath.Join(l.root, filepath.FromSlash(filepath.Clean(key))), nil
}

func (l *LocalFS) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	target, err := l.path(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", filepath.Dir(target), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".yreader-put-*")
	if err != nil {
		return fmt.Errorf("storage: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		_ = os.Remove(tmpName)
	}()

	written, err := io.Copy(tmp, r)
	if err != nil {
		return fmt.Errorf("storage: write %s: %w", key, err)
	}
	if size >= 0 && written != size {
		return fmt.Errorf("storage: size mismatch for %s: wrote %d, want %d", key, written, size)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("storage: sync %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close %s: %w", key, err)
	}
	// 同目录 Rename：同文件系统内原子替换，不会出现 EXDEV。
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("storage: rename %s: %w", key, err)
	}
	return nil
}

func (l *LocalFS) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	target, err := l.path(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (l *LocalFS) Delete(ctx context.Context, key string) error {
	target, err := l.path(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l *LocalFS) Exists(ctx context.Context, key string) (bool, error) {
	target, err := l.path(key)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, statErr := os.Stat(target)
	if statErr == nil {
		return true, nil
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return false, nil
	}
	return false, statErr
}

// DeletePrefix 递归删除前缀下的所有对象与空目录。
func (l *LocalFS) DeletePrefix(ctx context.Context, prefix string) error {
	cleanPrefix := strings.TrimSuffix(strings.TrimSpace(prefix), "/") + "/"
	if err := validateKey(strings.TrimSuffix(cleanPrefix, "/")); err != nil {
		return err
	}
	dir, err := l.path(strings.TrimSuffix(cleanPrefix, "/"))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
		return nil
	}

	var dirs []string
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		return os.Remove(p)
	})
	if walkErr != nil {
		return walkErr
	}
	// 自底向上删除目录（含空目录）；根目录本身也删除。
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	return nil
}
