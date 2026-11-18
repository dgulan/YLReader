package storage

import (
	"context"
	"io"
	"path/filepath"
)

// ReadBackupSecret reads through the same private-directory/file checks as
// control data. It never follows a public or linked key/DSN file.
func ReadBackupSecret(ctx context.Context, path string, max int64) ([]byte, error) {
	if !filepath.IsAbs(path) || max < 1 || max > 1<<20 {
		return nil, ErrInvalidKey
	}
	files, e := OpenBackupFiles(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	defer files.Close()
	r, e := files.Get(ctx, filepath.Base(path))
	if e != nil {
		return nil, e
	}
	defer r.Close()
	data, e := io.ReadAll(io.LimitReader(contextReaderPortable{ctx, r}, max+1))
	if e != nil {
		return nil, e
	}
	if int64(len(data)) > max {
		return nil, ErrUnsafeStorage
	}
	return data, nil
}

type contextReaderPortable struct {
	context.Context
	io.Reader
}

func (r contextReaderPortable) Read(p []byte) (int, error) {
	if e := r.Err(); e != nil {
		return 0, e
	}
	return r.Reader.Read(p)
}
