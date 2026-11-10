package sandbox

import (
	"container/list"
	"sync"

	"github.com/dop251/goja"
)

// programCache 是编译后 *goja.Program 的 LRU 缓存，同时受条目数与字节数上限约束。
type programCache struct {
	maxEntries int
	maxBytes   int64

	mu    sync.Mutex
	ll    *list.List
	m     map[string]*list.Element
	bytes int64
}

type programEntry struct {
	key     string
	program *goja.Program
	size    int64
}

func newProgramCache(maxEntries int, maxBytes int64) *programCache {
	return &programCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ll:         list.New(),
		m:          map[string]*list.Element{},
	}
}

// get 返回缓存命中的程序；未命中则编译并插入，随后按上限淘汰最久未使用项。
func (c *programCache) get(key string, compile func() (*goja.Program, error)) (*goja.Program, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.m[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*programEntry).program, nil
	}

	program, err := compile()
	if err != nil {
		return nil, err
	}

	size := int64(len(key))
	e := c.ll.PushFront(&programEntry{key: key, program: program, size: size})
	c.m[key] = e
	c.bytes += size
	c.evictLocked()
	return program, nil
}

func (c *programCache) evictLocked() {
	for c.ll.Len() > c.maxEntries || c.bytes > c.maxBytes {
		back := c.ll.Back()
		if back == nil {
			return
		}
		entry := back.Value.(*programEntry)
		c.ll.Remove(back)
		delete(c.m, entry.key)
		c.bytes -= entry.size
	}
}
