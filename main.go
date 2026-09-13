package main

import (
	"log"
	"os"
	"time"
	"urlshorter/internal/api"
	"urlshorter/internal/db"

	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	mgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	sredis "github.com/ulule/limiter/v3/drivers/store/redis"
)

func main() {
	dbConn, err := db.ConnectDB()
	db.InitRedis()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	cache := api.NewRedisCache(db.RedisClient, api.CacheConfig{
		FreshTTL: 10 * time.Minute,
		StaleTTL: 30 * time.Minute,
		Beta:     0.8,
	})
	cache.Db = dbConn
	api.InitBreakers()
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	rateStr := os.Getenv("RATE_LIMIT")
	if rateStr == "" {
		rateStr = "10000-S"
	}
	rate, err := limiter.NewRateFromFormatted(rateStr)
	if err != nil {
		rate, _ = limiter.NewRateFromFormatted("10000-S")
	}
	store, err := sredis.NewStoreWithOptions(db.RedisClient, limiter.StoreOptions{
		Prefix:   "limiter",
		MaxRetry: 3,
	})
	if err != nil {
		log.Fatalf("Failed to create redis limiter store: %v", err)
	}
	if os.Getenv("DISABLE_RATE_LIMIT") != "true" {
		middleware := mgin.NewMiddleware(limiter.New(store, rate))
		router.Use(middleware)
	}

	router.GET("/target", func(c *gin.Context) { c.String(200, "OK") })
	router.GET("/health", func(c *gin.Context) { api.HandleHealthCheck(c, dbConn, db.RedisClient) })
	router.POST("/shorten", func(c *gin.Context) { api.HandleUserLink(c, dbConn, db.RedisClient) })
	router.GET("/:code", func(c *gin.Context) { api.HandleRedirect(c, dbConn, cache) })

	api.StartOutboxWorker(dbConn, db.RedisClient)

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
}
