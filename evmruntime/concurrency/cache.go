// Package concurrency provides state that parallel callbacks can safely share.
package concurrency

import (
	"context"
	"sync"
	"time"
)

type version struct{ _ byte }

// Entry identifies one publication. Copies retain its identity.
type Entry[V any] struct {
	Value   V
	version *version
}

type item[V any] struct {
	entry  Entry[V]
	expiry time.Time
}

type flight[V any] struct {
	done  chan struct{}
	entry Entry[V]
}

// Cache synchronizes access to entries. Callers own the safety of native Go keys and values.
// Its zero value is ready to use. Do not copy a Cache after first use.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	items map[K]item[V]
	loads map[K]*flight[V]
}

func NewCache[K comparable, V any]() *Cache[K, V] { return new(Cache[K, V]) }

func (c *Cache[K, V]) get(key K) (Entry[V], bool) {
	v, ok := c.items[key]
	if ok && !v.expiry.IsZero() && !time.Now().Before(v.expiry) {
		delete(c.items, key)
		return Entry[V]{}, false
	}
	return v.entry, ok
}

func (c *Cache[K, V]) Get(key K) (Entry[V], bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.get(key)
}

const maxEntries = 1024

func (c *Cache[K, V]) put(key K, entry Entry[V], expiry time.Time) {
	if c.items == nil {
		c.items = make(map[K]item[V])
	}
	if !expiry.IsZero() && !time.Now().Before(expiry) {
		delete(c.items, key)
		return
	}
	if len(c.items) >= maxEntries {
		if _, exists := c.items[key]; !exists {
			// Reclaim an entry even when callers only insert new keys.
			for victim := range c.items {
				delete(c.items, victim)
				break
			}
		}
	}
	c.items[key] = item[V]{entry, expiry}
}

// Set replaces an entry. A zero expiry keeps it eligible until eviction or invalidation.
func (c *Cache[K, V]) Set(key K, value V, expiry time.Time) Entry[V] {
	if key != key {
		panic("concurrency.Cache requires a reflexive key")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := Entry[V]{value, new(version)}
	delete(c.loads, key) // an older loader can finish, but cannot overwrite this Set
	c.put(key, entry, expiry)
	return entry
}

func (c *Cache[K, V]) InvalidateIfCurrent(key K, entry Entry[V]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.version != nil && c.items[key].entry.version == entry.version {
		delete(c.items, key)
	}
}

// GetOrLoad runs load synchronously on a miss. Concurrent callers for the same key
// can cancel their wait independently. A failed or panicking load releases waiters
// to retry with their own loader. A loader must not wait on its own pending load.
func (c *Cache[K, V]) GetOrLoad(ctx context.Context, key K, load func() (V, time.Time, error)) (Entry[V], error) {
	if key != key {
		panic("concurrency.Cache requires a reflexive key")
	}
	for {
		if err := ctx.Err(); err != nil {
			return Entry[V]{}, err
		}
		c.mu.Lock()
		if entry, ok := c.get(key); ok {
			c.mu.Unlock()
			return entry, nil
		}
		if f := c.loads[key]; f != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return Entry[V]{}, ctx.Err()
			case <-f.done:
				if f.entry.version != nil {
					return f.entry, nil
				}
				continue
			}
		}
		f := &flight[V]{done: make(chan struct{})}
		if c.loads == nil {
			c.loads = make(map[K]*flight[V])
		}
		c.loads[key] = f
		c.mu.Unlock()
		var entry Entry[V]
		var expiry time.Time
		defer func() {
			c.mu.Lock()
			if c.loads[key] == f {
				if entry.version != nil {
					c.put(key, entry, expiry)
				}
				delete(c.loads, key)
			}
			f.entry = entry
			close(f.done)
			c.mu.Unlock()
		}()
		v, exp, err := load()
		if err != nil {
			return Entry[V]{}, err
		}
		entry = Entry[V]{v, new(version)}
		expiry = exp
		return entry, nil
	}
}
