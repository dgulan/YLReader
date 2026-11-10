// Package cache 提供正文缓存的最小抽象（Redis 与内存实现）。
//
// 生产使用 Redis（RESP 协议手写实现，避免为单一用途引入重型依赖）；
// 单元/集成测试使用 Memory 以保证可重复执行，二者行为一致：
// 命中返回值，未命中返回 ErrMiss。
package cache

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrMiss 表示缓存未命中。
var ErrMiss = errors.New("cache: miss")

// Cache 是正文缓存的最小接口。
type Cache interface {
	// Get 读取键值；未命中返回 ErrMiss。
	Get(ctx context.Context, key string) ([]byte, error)
	// SetEX 写入键值并设置过期时间。
	SetEX(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// Memory 是进程内缓存实现（仅用于测试与本地降级）。
type Memory struct {
	mu      sync.Mutex
	entries map[string]entry
	now     func() time.Time
}

type entry struct {
	value   []byte
	expires time.Time
}

// NewMemory 创建内存缓存。
func NewMemory() *Memory {
	return &Memory{entries: make(map[string]entry), now: time.Now}
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, ErrMiss
	}
	if !e.expires.IsZero() && m.now().After(e.expires) {
		delete(m.entries, key)
		return nil, ErrMiss
	}
	return append([]byte(nil), e.value...), nil
}

func (m *Memory) SetEX(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = entry{value: append([]byte(nil), value...), expires: m.now().Add(ttl)}
	return nil
}

// Len 返回当前条目数（测试辅助）。
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
