package concurrency

import (
	"evmruntime/concurrency/internal/cachecore"
	"time"
)

type Cache[K comparable, V any] struct{ store cachecore.Store }
type Entry[V any] struct {
	Value  V
	ticket cachecore.Ticket
}

func NewCache[K comparable, V any]() *Cache[K, V] {
	return &Cache[K, V]{store: cachecore.New()}
}
func (c *Cache[K, V]) Get(key K) (Entry[V], bool) {
	v, t, ok := cachecore.Get(c.store, key)
	if !ok {
		return Entry[V]{}, false
	}
	return Entry[V]{v.(V), t}, true
}
func (c *Cache[K, V]) Set(key K, v V, expiry time.Time) Entry[V] {
	return Entry[V]{v, cachecore.Set(c.store, key, v, expiry)}
}
func (c *Cache[K, V]) GetOrLoad(key K, fetch func() (V, time.Time, error)) (Entry[V], error) {
	v, t, err := cachecore.Load(c.store, key, func() (any, time.Time, error) { x, exp, err := fetch(); return x, exp, err })
	if err != nil {
		return Entry[V]{}, err
	}
	return Entry[V]{v.(V), t}, nil
}
func (c *Cache[K, V]) InvalidateIfCurrent(key K, e Entry[V]) {
	cachecore.Invalidate(c.store, key, e.ticket)
}
