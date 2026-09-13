package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"urlshorter/models"
)

var (
	testDB          *gorm.DB
	testRedisClient *redis.Client
	testCache       *RedisCache
	testRouter      *gin.Engine
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
	)
	if err != nil {
		os.Exit(1)
	}

	redisContainer, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		os.Exit(1)
	}

	pgConnStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = redisContainer.Terminate(ctx)
		os.Exit(1)
	}

	for i := 0; i < 30; i++ {
		dbRaw, rawErr := sql.Open("pgx", pgConnStr)
		if rawErr == nil {
			if pingErr := dbRaw.Ping(); pingErr == nil {
				_ = dbRaw.Close()
				break
			}
			_ = dbRaw.Close()
		}
		time.Sleep(300 * time.Millisecond)
	}

	testDB, err = gorm.Open(postgres.Open(pgConnStr), &gorm.Config{})
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = redisContainer.Terminate(ctx)
		os.Exit(1)
	}

	_ = testDB.AutoMigrate(&models.Link{})

	redisURI, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = redisContainer.Terminate(ctx)
		os.Exit(1)
	}

	redisOpts, err := redis.ParseURL(redisURI)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		_ = redisContainer.Terminate(ctx)
		os.Exit(1)
	}

	testRedisClient = redis.NewClient(redisOpts)
	InitBreakers()

	testCache = NewRedisCache(testRedisClient, CacheConfig{
		FreshTTL: 10 * time.Minute,
		StaleTTL: 30 * time.Minute,
	})
	testCache.Db = testDB

	outboxCtx, cancelOutbox := context.WithCancel(ctx)
	go testCache.OutboxWorker(outboxCtx)

	gin.SetMode(gin.TestMode)
	testRouter = gin.New()
	testRouter.Use(gin.Recovery())

	testRouter.GET("/target", func(c *gin.Context) { c.String(http.StatusOK, "OK") })
	testRouter.GET("/health", func(c *gin.Context) { HandleHealthCheck(c, testDB, testRedisClient) })
	testRouter.POST("/shorten", func(c *gin.Context) { HandleUserLink(c, testDB, testRedisClient) })
	testRouter.GET("/:code", func(c *gin.Context) { HandleRedirect(c, testDB, testCache) })

	code := m.Run()

	cancelOutbox()
	_ = testRedisClient.Close()
	sqlDB, _ := testDB.DB()
	if sqlDB != nil {
		_ = sqlDB.Close()
	}
	_ = pgContainer.Terminate(ctx)
	_ = redisContainer.Terminate(ctx)

	os.Exit(code)
}

func TestHealthCheck(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	testRouter.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp["status"])
	assert.Equal(t, "up", resp["database"])
	assert.Equal(t, "up", resp["redis"])
}

func TestShortenAndRedirect(t *testing.T) {
	targetURL := "https://example.com/original-destination"
	payload, _ := json.Marshal(map[string]string{"url": targetURL})

	req, _ := http.NewRequest(http.MethodPost, "/shorten", bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testRouter.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var shortenResp map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &shortenResp)
	require.NoError(t, err)

	shortURL := shortenResp["short_url"]
	require.NotEmpty(t, shortURL)
	shortCode := shortURL[strings.LastIndex(shortURL, "/")+1:]
	require.NotEmpty(t, shortCode)

	var link models.Link
	err = testDB.Where("short_code = ?", shortCode).First(&link).Error
	require.NoError(t, err)
	assert.Equal(t, targetURL, link.OriginalURL)

	reqRedirect, _ := http.NewRequest(http.MethodGet, "/"+shortCode, nil)
	wRedirect := httptest.NewRecorder()
	testRouter.ServeHTTP(wRedirect, reqRedirect)

	assert.Equal(t, http.StatusFound, wRedirect.Code)
	assert.Equal(t, targetURL, wRedirect.Header().Get("Location"))

	cachedURL, found := l1Cache.Get("slug:" + shortCode)
	assert.True(t, found)
	assert.Equal(t, targetURL, cachedURL)

	redisCached, err := testCache.client.Get(context.Background(), "slug:"+shortCode).Result()
	assert.NoError(t, err)
	assert.Contains(t, redisCached, targetURL)

	for i := 0; i < 5; i++ {
		wSubsequent := httptest.NewRecorder()
		testRouter.ServeHTTP(wSubsequent, reqRedirect)
		assert.Equal(t, http.StatusFound, wSubsequent.Code)
	}

	assert.Eventually(t, func() bool {
		val, err := testRedisClient.Get(context.Background(), "clicks:"+shortCode).Int()
		return err == nil && val > 0
	}, 5*time.Second, 100*time.Millisecond)

	assert.Eventually(t, func() bool {
		var updatedLink models.Link
		if err := testDB.Where("short_code = ?", shortCode).First(&updatedLink).Error; err != nil {
			return false
		}
		return updatedLink.Clicks > 0
	}, 5*time.Second, 100*time.Millisecond)
}

func TestCacheFallbackToDatabase(t *testing.T) {
	fallbackCode := "fallback01"
	fallbackURL := "https://fallback.example.org"
	err := testDB.Create(&models.Link{
		ShortCode:   fallbackCode,
		OriginalURL: fallbackURL,
		Clicks:      0,
	}).Error
	require.NoError(t, err)

	l1Cache.mu.Lock()
	delete(l1Cache.items, "slug:"+fallbackCode)
	l1Cache.mu.Unlock()
	_ = testRedisClient.Del(context.Background(), "slug:"+fallbackCode).Err()

	req, _ := http.NewRequest(http.MethodGet, "/"+fallbackCode, nil)
	w := httptest.NewRecorder()
	testRouter.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, fallbackURL, w.Header().Get("Location"))

	recoveredURL, ok := l1Cache.Get("slug:" + fallbackCode)
	assert.True(t, ok)
	assert.Equal(t, fallbackURL, recoveredURL)
}
