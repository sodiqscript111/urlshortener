package api

import (
	"context"
	"github.com/goccy/go-json"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"urlshorter/models"
	"urlshorter/utils"
)

var (
	ctx        = context.Background()
	l1Cache    = NewBoundedL1Cache(50000)
	clickQueue = make(chan string, 100000)
)

type CacheConfig struct {
	FreshTTL time.Duration
	StaleTTL time.Duration
	Beta     float64
}

type CacheEntry struct {
	URL        string `json:"url"`
	FreshUntil int64  `json:"fresh_until"`
	StaleUntil int64  `json:"stale_until"`
}

type RedisCache struct {
	client *redis.Client
	config CacheConfig
	group  singleflight.Group
	Db     *gorm.DB
}

type CacheRepository interface {
	Set(ctx context.Context, key string, url string) error
	Get(ctx context.Context, key string) (string, error)
	IncrClicks(ctx context.Context, key string) error
	PushOutbox(ctx context.Context, code string) error
}

var clickBufferStarted sync.Once

func (c *RedisCache) PushOutbox(ctx context.Context, code string) error {
	select {
	case clickQueue <- code:
		return nil
	default:
		return nil
	}
}

func (c *RedisCache) StartClickBufferWorker(ctx context.Context) {
	clickBufferStarted.Do(func() {
		go func() {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()

			var batch []interface{}
			flush := func() {
				if len(batch) == 0 {
					return
				}
				_ = c.client.RPush(ctx, "outbox:clicks", batch...).Err()
				batch = batch[:0]
			}

			for {
				select {
				case code := <-clickQueue:
					batch = append(batch, code)
					if len(batch) >= 500 {
						flush()
					}
				case <-ticker.C:
					flush()
				}
			}
		}()
	})
}

func NewRedisCache(client *redis.Client, config CacheConfig) *RedisCache {
	rc := &RedisCache{
		client: client,
		config: config,
	}
	rc.StartClickBufferWorker(context.Background())
	return rc
}

func (c *RedisCache) Set(ctx context.Context, key string, url string) error {
	now := time.Now().Unix()
	l1Cache.Set(key, url, 30*time.Second)

	freshTTL := c.config.FreshTTL
	if c.config.Beta > 0 {
		freshTTL = time.Duration(float64(c.config.FreshTTL) * (1 - c.config.Beta*rand.Float64()))
	}
	entry := CacheEntry{
		URL:        url,
		FreshUntil: now + int64(freshTTL.Seconds()),
		StaleUntil: now + int64((freshTTL + c.config.StaleTTL).Seconds()),
	}
	jsonEntry, err := json.Marshal(entry)
	if err != nil {
		log.Println("Failed to marshal CacheEntry:", err)
		return err
	}
	return c.client.Set(ctx, key, jsonEntry, 0).Err()
}

func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	now := time.Now().Unix()

	// 1. In-Memory L1 Cache (sub-microsecond, 0 network calls, bounded to 50k items)
	if cachedURL, ok := l1Cache.Get(key); ok {
		return cachedURL, nil
	}

	// 2. L2 Redis Cache
	jsonEntry, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return c.refresh(ctx, key, "")
	} else if err != nil {
		return "", err
	}

	var entry CacheEntry
	if err := json.Unmarshal(jsonEntry, &entry); err != nil {
		return "", err
	}

	// Populate L1 cache for 30s
	l1Cache.Set(key, entry.URL, 30*time.Second)

	if now < entry.FreshUntil {
		return entry.URL, nil
	}

	if now < entry.StaleUntil {
		go func() {
			_, err, _ := c.group.Do(key, func() (interface{}, error) {
				return c.refresh(ctx, key, entry.URL)
			})
			if err != nil {
				log.Println("Background refresh failed:", err)
			}
		}()
		return entry.URL, nil
	}

	return c.refresh(ctx, key, "")
}

func (c *RedisCache) refresh(ctx context.Context, key string, staleURL string) (string, error) {
	url, err, _ := c.group.Do(key, func() (interface{}, error) {
		jsonEntry, err := c.client.Get(ctx, key).Bytes()
		if err == nil {
			var updatedEntry CacheEntry
			json.Unmarshal(jsonEntry, &updatedEntry)
			if time.Now().Unix() < updatedEntry.FreshUntil {
				log.Println("Already refreshed:", key)
				return updatedEntry.URL, nil
			}
		}

		var link models.Link
		_, err = DBCircuitBreaker.Execute(func() (interface{}, error) {
			return nil, c.Db.Where("short_code = ?", key[5:]).First(&link).Error
		})
		if err != nil {
			log.Println("DB miss or circuit open:", key, err)
			if staleURL != "" {
				return staleURL, nil
			}
			return "", err
		}

		if err := c.Set(ctx, key, link.OriginalURL); err != nil {
			log.Println("Failed to update Redis:", err)
			if staleURL != "" {
				return staleURL, nil
			}
			return "", err
		}
		log.Println("Refreshed:", key, link.OriginalURL)
		return link.OriginalURL, nil
	})
	if err != nil {
		if staleURL != "" {
			return staleURL, nil
		}
		return "", err
	}
	if url == nil {
		return "", nil
	}
	return url.(string), nil
}

func (c *RedisCache) IncrClicks(ctx context.Context, key string) error {
	return c.client.Incr(ctx, key).Err()
}

func (c *RedisCache) OutboxWorker(ctx context.Context) {
	for {
		codes, err := c.client.LPopCount(ctx, "outbox:clicks", 1000).Result()
		if err != nil && err != redis.Nil {
			time.Sleep(time.Second)
			continue
		}

		if len(codes) == 0 {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		clickCounts := make(map[string]int)
		for _, code := range codes {
			clickCounts[code]++
		}

		pipe := c.client.Pipeline()
		for code, count := range clickCounts {
			pipe.IncrBy(ctx, "clicks:"+code, int64(count))
		}
		_, _ = pipe.Exec(ctx)

		for code, count := range clickCounts {
			_, _ = DBCircuitBreaker.Execute(func() (interface{}, error) {
				return nil, c.Db.Model(&models.Link{}).Where("short_code = ?", code).
					Update("clicks", gorm.Expr("clicks + ?", count)).Error
			})
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func HandleUserLink(c *gin.Context, db *gorm.DB, redisClient *redis.Client) {
	type input struct {
		URL string `json:"url"`
	}
	var req input
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Println("Invalid JSON input")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	if _, err := url.ParseRequestURI(req.URL); err != nil {
		log.Println("Invalid URL provided:", req.URL)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid URL"})
		return
	}

	code, err := utils.CryptoRandomString(10)
	if err != nil {
		log.Println("Failed to generate short code:", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate code"})
		return
	}

	saveLink := models.Link{
		OriginalURL: req.URL,
		ShortCode:   code,
		Clicks:      0,
	}
	_, err = DBCircuitBreaker.Execute(func() (interface{}, error) {
		return nil, db.Create(&saveLink).Error
	})
	if err != nil {
		log.Println("Failed to save link to DB:", err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to save URL: " + err.Error()})
		return
	}

	log.Println("URL Saved to DB with Code:", code)

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080/"
	}

	c.JSON(http.StatusOK, gin.H{"short_url": baseURL + code})
}

func HandleRedirect(c *gin.Context, db *gorm.DB, cache CacheRepository) {
	code := c.Param("code")

	url, err := cache.Get(ctx, "slug:"+code)
	if err != nil || url == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Short URL not found"})
		return
	}

	_ = cache.PushOutbox(ctx, code)

	c.Header("Cache-Control", "public, max-age=60")
	c.Redirect(http.StatusFound, url)
}

func StartOutboxWorker(db *gorm.DB, redisClient *redis.Client) {
	cache := NewRedisCache(redisClient, CacheConfig{})
	cache.Db = db
	go cache.OutboxWorker(ctx)
}

func HandleHealthCheck(c *gin.Context, db *gorm.DB, redisClient *redis.Client) {
	sqlDB, err := db.DB()
	dbStatus := "up"
	if err != nil || sqlDB.Ping() != nil {
		dbStatus = "down"
	}

	redisStatus := "up"
	if err := redisClient.Ping(c.Request.Context()).Err(); err != nil {
		redisStatus = "down"
	}

	status := http.StatusOK
	if dbStatus == "down" || redisStatus == "down" {
		status = http.StatusServiceUnavailable
	}

	c.JSON(status, gin.H{
		"status":   "ok",
		"database": dbStatus,
		"redis":    redisStatus,
	})
}
