package api

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"urlshorter/models"
	"urlshorter/utils"
)

var ctx = context.Background()

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
}

func NewRedisCache(client *redis.Client, config CacheConfig) *RedisCache {
	return &RedisCache{
		client: client,
		config: config,
	}
}

func (c *RedisCache) Set(ctx context.Context, key string, url string) error {
	now := time.Now().Unix()
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
	log.Println("Caching URL in Redis:", key, url, "Fresh until:", entry.FreshUntil)
	return c.client.Set(ctx, key, jsonEntry, 0).Err()
}

func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	jsonEntry, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		log.Println("Cache miss:", key)
		return c.refresh(ctx, key, "")
	} else if err != nil {
		log.Println("Redis error:", err)
		return "", err
	}

	var entry CacheEntry
	if err := json.Unmarshal(jsonEntry, &entry); err != nil {
		log.Println("Unmarshal error:", err)
		return "", err
	}

	now := time.Now().Unix()

	if now < entry.FreshUntil {
		log.Println("Cache hit (fresh):", key, entry.URL)
		return entry.URL, nil
	}

	if now < entry.StaleUntil {
		log.Println("Cache hit (stale):", key, entry.URL)
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

	log.Println("Cache expired:", key)
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
		if err := c.Db.Where("short_code = ?", key[5:]).First(&link).Error; err != nil {
			log.Println("DB miss:", key, err)
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
		return staleURL, nil
	}
	return url.(string), nil
}

func (c *RedisCache) IncrClicks(ctx context.Context, key string) error {
	return c.client.Watch(ctx, func(tx *redis.Tx) error {
		clicks, err := tx.Get(ctx, key).Int64()
		if err != nil && err != redis.Nil {
			return err
		}
		_, err = tx.Pipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, clicks+1, 0)
			return nil
		})
		return err
	}, key)
}

func (c *RedisCache) OutboxWorker(ctx context.Context) {
	for {
		var outbox []models.ClickOutbox
		if err := c.Db.Where("processed = ?", false).Find(&outbox).Error; err != nil {
			log.Println("Outbox fetch error:", err)
			time.Sleep(time.Second)
			continue
		}
		for _, entry := range outbox {
			if err := c.IncrClicks(ctx, "clicks:"+entry.ShortCode); err != nil {
				log.Println("Outbox Redis update failed:", err)
				continue
			}
			if err := c.Db.Model(&models.Link{}).Where("short_code = ?", entry.ShortCode).
				Update("clicks", gorm.Expr("clicks + ?", entry.ClickCount)).Error; err != nil {
				log.Println("Outbox DB update failed:", err)
				continue
			}
			c.Db.Model(&entry).Update("processed", true)
			log.Println("Outbox processed:", entry.ShortCode)
		}
		time.Sleep(time.Second)
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
	if err := db.Create(&saveLink).Error; err != nil {
		log.Println("Failed to save link to DB:", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save URL: " + err.Error()})
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
	if err != nil {
		log.Println("Redirect failed:", code, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Short URL not found"})
		return
	}

	if err := db.Create(&models.ClickOutbox{
		ShortCode:  code,
		ClickCount: 1,
		Processed:  false,
	}).Error; err != nil {
		log.Println("Failed to log click to outbox:", err)
	} else {
		log.Println("Click logged to outbox:", code)
	}

	if err := cache.IncrClicks(ctx, "clicks:"+code); err != nil {
		log.Println("Failed to increment Redis clicks:", err)
	}

	log.Println("Redirecting to:", url)
	c.Redirect(http.StatusMovedPermanently, url)
}

func StartOutboxWorker(db *gorm.DB, redisClient *redis.Client) {
	cache := NewRedisCache(redisClient, CacheConfig{})
	cache.Db = db
	go cache.OutboxWorker(ctx)
}
