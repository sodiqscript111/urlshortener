package api

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"urlshorter/services"
)

type LinkHandler struct {
	service services.LinkService
	db      *gorm.DB
	redis   *redis.Client
}

func NewLinkHandler(service services.LinkService, db *gorm.DB, redisClient *redis.Client) *LinkHandler {
	return &LinkHandler{
		service: service,
		db:      db,
		redis:   redisClient,
	}
}

type ShortenRequest struct {
	URL string `json:"url" binding:"required"`
}

func (h *LinkHandler) Shorten(c *gin.Context) {
	var req ShortenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	code, err := h.service.Shorten(c.Request.Context(), req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080/"
	}

	c.JSON(http.StatusOK, gin.H{"short_url": baseURL + code})
}

func (h *LinkHandler) Redirect(c *gin.Context) {
	code := c.Param("code")
	targetURL, err := h.service.Resolve(c.Request.Context(), code)
	if err != nil || targetURL == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Short URL not found"})
		return
	}

	c.Header("Cache-Control", "public, max-age=60")
	c.Redirect(http.StatusFound, targetURL)
}

func (h *LinkHandler) HealthCheck(c *gin.Context) {
	dbStatus := "up"
	if h.db != nil {
		sqlDB, err := h.db.DB()
		if err != nil || sqlDB.Ping() != nil {
			dbStatus = "down"
		}
	}

	redisStatus := "up"
	if h.redis != nil {
		if err := h.redis.Ping(c.Request.Context()).Err(); err != nil {
			redisStatus = "down"
		}
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

func (h *LinkHandler) Target(c *gin.Context) {
	c.String(http.StatusOK, "OK")
}
