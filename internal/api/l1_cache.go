package api

import (
	"sync"
	"time"
)

type l1CacheEntry struct {
	url     string
	expires int64
}

// BoundedL1Cache is a thread-safe, size-capped in-memory cache with TTL and active eviction.
type BoundedL1Cache struct {
	mu          sync.RWMutex
	items       map[string]l1CacheEntry
	maxCapacity int
}

// NewBoundedL1Cache creates a bounded L1 cache capped at maxCapacity items.
func NewBoundedL1Cache(maxCapacity int) *BoundedL1Cache {
	if maxCapacity <= 0 {
		maxCapacity = 50000 // default 50,000 items (~5MB RAM max)
	}
	c := &BoundedL1Cache{
		items:       make(map[string]l1CacheEntry, maxCapacity),
		maxCapacity: maxCapacity,
	}

	// Active background cleanup every 10 seconds to reclaim memory from expired keys
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.evictExpired()
		}
	}()

	return c
}

// Get retrieves an unexpired entry from L1 cache.
func (c *BoundedL1Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		return "", false
	}
	if time.Now().Unix() >= entry.expires {
		// Lazily remove expired entry
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return "", false
	}
	return entry.url, true
}

// Set stores a key-value pair with TTL, strictly enforcing the maxCapacity cap.
func (c *BoundedL1Cache) Set(key string, url string, ttl time.Duration) {
	now := time.Now().Unix()
	expires := now + int64(ttl.Seconds())

	c.mu.Lock()
	defer c.mu.Unlock()

	// If capacity reached, evict expired entries first
	if len(c.items) >= c.maxCapacity {
		for k, v := range c.items {
			if now >= v.expires {
				delete(c.items, k)
			}
		}
	}

	// If still at or above capacity, drop any arbitrary key to strictly honor the cap
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

// evictExpired purges expired keys in the background
func (c *BoundedL1Cache) evictExpired() {
	now := time.Now().Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.items {
		if now >= v.expires {
			delete(c.items, k)
		}
	}
}

// Len returns the current number of cached items
func (c *BoundedL1Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}
