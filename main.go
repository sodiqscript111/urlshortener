package main

import (
	"github.com/gin-gonic/gin"
	"log"
	"urlshorter/internal/api"
	"urlshorter/internal/db"
)

func main() {
	dbConn, err := db.ConnectDB() // Capture *gorm.DB
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	router := gin.Default()
	router.POST("/shorten", func(c *gin.Context) { api.HandleUserLink(c, dbConn) })
	router.Run(":8080")
}
