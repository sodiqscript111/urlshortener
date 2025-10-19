package main

import (
	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	mgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	"github.com/ulule/limiter/v3/drivers/store/memory"
	"log"
	"urlshorter/internal/api"
	"urlshorter/internal/db"
)

func main() {
	dbConn, err := db.ConnectDB()
	db.InitRedis()
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	router := gin.Default()

	rate, _ := limiter.NewRateFromFormatted("10-M")
	store := memory.NewStore()
	middleware := mgin.NewMiddleware(limiter.New(store, rate))

	router.Use(middleware)

	router.POST("/shorten", func(c *gin.Context) { api.HandleUserLink(c, dbConn, db.RedisClient) })
	router.GET("/:code", func(c *gin.Context) { api.HandleRedirect(c, dbConn, db.RedisClient) })

	router.Run(":8080")
}
