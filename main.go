package main

import (
	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	mgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	"github.com/ulule/limiter/v3/drivers/store/memory"
	"log"
	"time"
	"urlshorter/internal/api"
	"urlshorter/internal/db"
)

func main() {
	dbConn, err := db.ConnectDB()
	db.InitRedis()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	cache := api.NewRedisCache(db.RedisClient, api.CacheConfig{
		FreshTTL: 5 * time.Second,
		StaleTTL: 10 * time.Second,
		Beta:     0.8,
	})
	cache.Db = dbConn
	router := gin.Default()

	rate, _ := limiter.NewRateFromFormatted("10-M")
	store := memory.NewStore()
	middleware := mgin.NewMiddleware(limiter.New(store, rate))

	router.Use(middleware)

	router.POST("/shorten", func(c *gin.Context) { api.HandleUserLink(c, dbConn, db.RedisClient) })
	router.GET("/:code", func(c *gin.Context) { api.HandleRedirect(c, dbConn, cache) })

	router.Run(":8080")
}
