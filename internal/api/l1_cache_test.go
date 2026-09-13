package api

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestL1Cache_Basic(t *testing.T) {
	c := NewBoundedL1Cache(100)
	c.Set("key1", "https://example.com/1", 5*time.Second)

	val, ok := c.Get("key1")
	assert.True(t, ok)
	assert.Equal(t, "https://example.com/1", val)

	_, ok = c.Get("nonexistent")
	assert.False(t, ok)
}

func TestL1Cache_Expiration(t *testing.T) {
	c := NewBoundedL1Cache(100)
	c.Set("expiring_key", "https://example.com/expired", 50*time.Millisecond)

	val, ok := c.Get("expiring_key")
	assert.True(t, ok)
	assert.Equal(t, "https://example.com/expired", val)

	time.Sleep(100 * time.Millisecond)

	_, ok = c.Get("expiring_key")
	assert.False(t, ok)
}

func TestL1Cache_CapacityCap(t *testing.T) {
	capacity := 5
	c := NewBoundedL1Cache(capacity)

	for i := 0; i < 15; i++ {
		c.Set(fmt.Sprintf("key%d", i), fmt.Sprintf("https://example.com/%d", i), 10*time.Second)
	}

	assert.LessOrEqual(t, c.Len(), capacity)
}

func TestL1Cache_Concurrency(t *testing.T) {
	c := NewBoundedL1Cache(1000)
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("concurrent_%d", id%10)
			c.Set(key, fmt.Sprintf("https://example.com/%d", id), 5*time.Second)
			_, _ = c.Get(key)
		}(i)
	}

	wg.Wait()
	assert.True(t, c.Len() > 0)
}
