package repository

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

type CacheEntry struct {
	URL        string `json:"url"`
	FreshUntil int64  `json:"fresh_until"`
	StaleUntil int64  `json:"stale_until"`
}

type CacheRepository interface {
	Get(ctx context.Context, key string) (*CacheEntry, error)
	Set(ctx context.Context, key string, entry *CacheEntry) error
	PushClickBatch(ctx context.Context, batch []interface{}) error
	PopClickBatch(ctx context.Context, count int64) ([]string, error)
	IncrementClicks(ctx context.Context, counts map[string]int) error
}

type redisCacheRepository struct {
	client *redis.Client
}

func NewRedisCacheRepository(client *redis.Client) CacheRepository {
	return &redisCacheRepository{client: client}
}

func (r *redisCacheRepository) Get(ctx context.Context, key string) (*CacheEntry, error) {
	data, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

func (r *redisCacheRepository) Set(ctx context.Context, key string, entry *CacheEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, data, 0).Err()
}

func (r *redisCacheRepository) PushClickBatch(ctx context.Context, batch []interface{}) error {
	if len(batch) == 0 {
		return nil
	}
	return r.client.RPush(ctx, "outbox:clicks", batch...).Err()
}

func (r *redisCacheRepository) PopClickBatch(ctx context.Context, count int64) ([]string, error) {
	codes, err := r.client.LPopCount(ctx, "outbox:clicks", int(count)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return codes, err
}

func (r *redisCacheRepository) IncrementClicks(ctx context.Context, counts map[string]int) error {
	pipe := r.client.Pipeline()
	for code, count := range counts {
		pipe.IncrBy(ctx, "clicks:"+code, int64(count))
	}
	_, err := pipe.Exec(ctx)
	return err
}
