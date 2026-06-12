package handler

import (
	"sync"
)

// ShardCount defines the number of shards
const ShardCount = 16

// ShardedMap is a sharded concurrent-safe Map
// Reduce lock contention by spreading data across multiple shards
type ShardedMap[V any] struct {
	shards [ShardCount]struct {
		mu   sync.RWMutex
		data map[string]V
	}
}

// NewShardedMap creates a new sharded Map
func NewShardedMap[V any]() *ShardedMap[V] {
	m := &ShardedMap[V]{}
	for i := 0; i < ShardCount; i++ {
		m.shards[i].data = make(map[string]V)
	}
	return m
}

// fnv1aHash uses the FNV-1a hashing algorithm to calculate the hash value of a string
func fnv1aHash(key string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return h
}

// getShard gets the corresponding shard index based on key
func (m *ShardedMap[V]) getShard(key string) int {
	return int(fnv1aHash(key) % ShardCount)
}

// Get Get value
func (m *ShardedMap[V]) Get(key string) (V, bool) {
	idx := m.getShard(key)
	m.shards[idx].mu.RLock()
	val, ok := m.shards[idx].data[key]
	m.shards[idx].mu.RUnlock()
	return val, ok
}

// Set set value
func (m *ShardedMap[V]) Set(key string, value V) {
	idx := m.getShard(key)
	m.shards[idx].mu.Lock()
	m.shards[idx].data[key] = value
	m.shards[idx].mu.Unlock()
}

// Delete delete value
func (m *ShardedMap[V]) Delete(key string) {
	idx := m.getShard(key)
	m.shards[idx].mu.Lock()
	delete(m.shards[idx].data, key)
	m.shards[idx].mu.Unlock()
}

// Range traverses all shards
// If fn returns false, stop traversing
func (m *ShardedMap[V]) Range(fn func(key string, value V) bool) {
	for i := 0; i < ShardCount; i++ {
		m.shards[i].mu.RLock()
		for k, v := range m.shards[i].data {
			if !fn(k, v) {
				m.shards[i].mu.RUnlock()
				return
			}
		}
		m.shards[i].mu.RUnlock()
	}
}

// Compute atomically reads, transforms, and writes a value under the shard lock.
// fn receives the current value and whether it exists. It returns the new value
// and whether to keep it (false = delete the key).
// The returned values are the original value and whether the key existed before.
func (m *ShardedMap[V]) Compute(key string, fn func(value V, exists bool) (V, bool)) (V, bool) {
	idx := m.getShard(key)
	m.shards[idx].mu.Lock()
	old, existed := m.shards[idx].data[key]
	newVal, keep := fn(old, existed)
	if keep {
		m.shards[idx].data[key] = newVal
	} else if existed {
		delete(m.shards[idx].data, key)
	}
	m.shards[idx].mu.Unlock()
	return old, existed
}

// Len returns the total number of entries across all shards.
func (m *ShardedMap[V]) Len() int {
	n := 0
	for i := 0; i < ShardCount; i++ {
		m.shards[i].mu.RLock()
		n += len(m.shards[i].data)
		m.shards[i].mu.RUnlock()
	}
	return n
}

// RangeDelete iterates all entries with write locks and deletes entries
// for which fn returns true.
func (m *ShardedMap[V]) RangeDelete(fn func(key string, value V) bool) {
	for i := 0; i < ShardCount; i++ {
		m.shards[i].mu.Lock()
		for k, v := range m.shards[i].data {
			if fn(k, v) {
				delete(m.shards[i].data, k)
			}
		}
		m.shards[i].mu.Unlock()
	}
}
