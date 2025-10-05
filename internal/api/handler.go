package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"net/url"
	"urlshorter/models"

	"urlshorter/utils"
)

func HandleUserLink(c *gin.Context, db *gorm.DB) {
	// Input struct for JSON binding
	type input struct {
		URL string `json:"url"`
	}
	var req input
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// Validate URL
	if _, err := url.ParseRequestURI(req.URL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid URL"})
		return
	}

	// Generate short code
	code, err := utils.CryptoRandomString(10)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate code"})
		return
	}

	// Save to Postgres
	saveLink := models.Link{
		OriginalURL: req.URL,
		ShortCode:   code,
		Clicks:      0,
	}
	if err := db.Create(&saveLink).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save URL: " + err.Error()})
		return
	}

	// Return short URL
	c.JSON(http.StatusOK, gin.H{"short_url": "yourdomain.com/" + code})
}
