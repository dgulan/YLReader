// Package cache 提供正文缓存的最小抽象（Redis 与内存实现）。
//
// 生产使用 Redis（RESP 协议手写实现，避免为单一用途引入重型依赖）；
// 单元/集成测试使用 Memory 以保证可重复执行，二者行为一致：
// 命中返回值，未命中返回 ErrMiss。
package cache

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

const defaultMemoryMaxBytes int64 = 64 << 20

// ErrMiss 表示缓存未命中。
var ErrMiss = errors.New("cache: miss")

// Cache 是正文缓存的最小接口。
type Cache interface {
	// Get 读取键值；未命中返回 ErrMiss。
	Get(ctx context.Context, key string) ([]byte, error)
	// SetEX 写入键值并设置过期时间。
	SetEX(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// Memory 是进程内缓存实现（仅用于测试与本地降级），带字节上限与过期/最老驱逐。
type Memory struct {
	mu        sync.Mutex
	entries   map[string]*list.Element
	order     *list.List // front = oldest, back = newest
	usedBytes int64
	maxBytes  int64
	now       func() time.Time
}

type entry struct {
	key     string
	value   []byte
	expires time.Time
	size    int64
}

// NewMemory 创建内存缓存（默认 64 MiB 上限）。
func NewMemory() *Memory {
	return NewMemoryWithLimit(defaultMemoryMaxBytes)
}

// NewMemoryWithLimit 创建指定字节上限的内存缓存。
func NewMemoryWithLimit(maxBytes int64) *Memory {
	if maxBytes <= 0 {
		maxBytes = defaultMemoryMaxBytes
	}
	return &Memory{
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		maxBytes: maxBytes,
		now:      time.Now,
	}
}

func (m *Memory) removeElement(elem *list.Element) {
	e := elem.Value.(*entry)
	delete(m.entries, e.key)
	m.order.Remove(elem)
	m.usedBytes -= e.size
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	elem, ok := m.entries[key]
	if !ok {
		return nil, ErrMiss
	}
	e := elem.Value.(*entry)
	if !e.expires.IsZero() && m.now().After(e.expires) {
		m.removeElement(elem)
		return nil, ErrMiss
	}
	m.order.MoveToBack(elem)
	return append([]byte(nil), e.value...), nil
}

func (m *Memory) SetEX(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.entries[key]; ok {
		m.removeElement(existing)
	}

	itemSize := int64(len(key) + len(value))
	if itemSize > m.maxBytes {
		return nil
	}

	now := m.now()
	if m.usedBytes+itemSize > m.maxBytes {
		for elem := m.order.Front(); elem != nil; {
			next := elem.Next()
			e := elem.Value.(*entry)
			if !e.expires.IsZero() && now.After(e.expires) {
				m.removeElement(elem)
			}
			elem = next
		}
		for m.usedBytes+itemSize > m.maxBytes && m.order.Len() > 0 {
			m.removeElement(m.order.Front())
		}
	}

	e := &entry{
		key:     key,
		value:   append([]byte(nil), value...),
		expires: now.Add(ttl),
		size:    itemSize,
	}
	m.entries[key] = m.order.PushBack(e)
	m.usedBytes += itemSize
	return nil
}

// Len 返回当前条目数（测试辅助）。
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// UsedBytes 返回当前缓存占用的字节数（测试辅助）。
func (m *Memory) UsedBytes() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usedBytes
}
