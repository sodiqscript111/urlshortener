package main

import (
	"context"
	"log"
	"os"
	"urlshorter/internal/api"
	"urlshorter/internal/db"
	"urlshorter/internal/repository"
	"urlshorter/services"

	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	mgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	sredis "github.com/ulule/limiter/v3/drivers/store/redis"
)

func main() {
	dbConn, err := db.ConnectDB()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	db.InitRedis()

	api.InitBreakers()

	linkRepo := repository.NewPostgresLinkRepository(dbConn)
	cacheRepo := repository.NewRedisCacheRepository(db.RedisClient)
	l1Cache := api.NewBoundedL1Cache(50000)

	linkService := services.NewLinkService(linkRepo, cacheRepo, l1Cache)
	linkService.StartWorkers(context.Background())

	linkHandler := api.NewLinkHandler(linkService, dbConn, db.RedisClient)

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

	router.GET("/target", linkHandler.Target)
	router.GET("/health", linkHandler.HealthCheck)
	router.POST("/shorten", linkHandler.Shorten)
	router.GET("/:code", linkHandler.Redirect)

	if err := router.Run(":8080"); err != nil {
		log.Fatalf("Server stopped: %v", err)
	}
}
