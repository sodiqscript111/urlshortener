package main

import (
	"context"
	"log"
	"time"
	"urlshorter/internal/api"
	"urlshorter/internal/db"

	"github.com/gin-gonic/gin"
	outray "github.com/sodiqscript111/outray-go"
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
		FreshTTL: 5 * time.Second,
		StaleTTL: 10 * time.Second,
		Beta:     0.8,
	})
	cache.Db = dbConn
	api.InitBreakers()
	router := gin.Default()

	rate, _ := limiter.NewRateFromFormatted("10-M")
	store, err := sredis.NewStoreWithOptions(db.RedisClient, limiter.StoreOptions{
		Prefix:   "limiter",
		MaxRetry: 3,
	})
	if err != nil {
		log.Fatalf("Failed to create redis limiter store: %v", err)
	}
	middleware := mgin.NewMiddleware(limiter.New(store, rate))

	router.Use(middleware)

	router.POST("/shorten", func(c *gin.Context) { api.HandleUserLink(c, dbConn, db.RedisClient) })
	router.GET("/:code", func(c *gin.Context) { api.HandleRedirect(c, dbConn, cache) })


	go func() {
		client := outray.NewClient(
			outray.WithServerURL("wss://api.outray.dev"),
			outray.WithAPIKey("outray_fbcd47c2dbccbe62b6cf61000dce8b8739e630547bf1dfaf6bf6ed4935bbb3d5"),
			outray.WithProtocol("http"),
			outray.WithPort(8080),
			outray.WithOnOpen(func(url string) {
				log.Printf("Tunnel Online: %s", url)
			}),
		)

		if err := client.Connect(context.Background()); err != nil {
			log.Printf("Tunnel error: %v", err)
		}
	}()

	router.Run(":8080")
}
