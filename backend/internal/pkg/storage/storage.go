// Package storage 定义对象/文件存储抽象，用于本地书正文、封面、字体与备份包。
//
// 生产可按部署形态选择 LocalFS（NAS / Docker Volume）或 S3（MinIO / 云对象存储）；
// 业务代码只依赖 Provider 接口，禁止硬编码本地路径。
package storage

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrInvalidKey 表示传入的对象键非法（绝对路径、含 .. 或 NUL 等）。
	ErrInvalidKey = errors.New("storage: invalid key")
	// ErrNotFound 表示对象不存在。
	ErrNotFound = errors.New("storage: not found")
)

// Provider 是存储后端的最小接口。
type Provider interface {
	// Put 写入对象；size 用于后端校验或分片预分配。
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	// Get 读取对象；不存在时返回 ErrNotFound。
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete 删除单个对象；不存在视为成功。
	Delete(ctx context.Context, key string) error
	// Exists 判断对象是否存在。
	Exists(ctx context.Context, key string) (bool, error)
	// DeletePrefix 递归删除目录前缀（含空目录）；前缀本身必须合法。
	DeletePrefix(ctx context.Context, prefix string) error
}
