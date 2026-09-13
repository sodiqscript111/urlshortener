package api

import (
	"sync"
	"time"
)

type l1CacheEntry struct {
	url     string
	expires int64
}

type BoundedL1Cache struct {
	mu          sync.RWMutex
	items       map[string]l1CacheEntry
	maxCapacity int
}

func NewBoundedL1Cache(maxCapacity int) *BoundedL1Cache {
	if maxCapacity <= 0 {
		maxCapacity = 50000
	}
	c := &BoundedL1Cache{
		items:       make(map[string]l1CacheEntry, maxCapacity),
		maxCapacity: maxCapacity,
	}

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.evictExpired()
		}
	}()

	return c
}

func (c *BoundedL1Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		return "", false
	}
	if time.Now().UnixMilli() >= entry.expires {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return "", false
	}
	return entry.url, true
}

func (c *BoundedL1Cache) Set(key string, url string, ttl time.Duration) {
	now := time.Now().UnixMilli()
	expires := now + ttl.Milliseconds()

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.items) >= c.maxCapacity {
		for k, v := range c.items {
			if now >= v.expires {
				delete(c.items, k)
			}
		}
	}

	if len(c.items) >= c.maxCapacity {
		for k := range c.items {
			delete(c.items, k)
			break
		}
	}

	c.items[key] = l1CacheEntry{
		url:     url,
		expires: expires,
	}
}

func (c *BoundedL1Cache) evictExpired() {
	now := time.Now().UnixMilli()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.items {
		if now >= v.expires {
			delete(c.items, k)
		}
	}
}

func (c *BoundedL1Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}
