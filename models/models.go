package models

import (
	"gorm.io/gorm"
	"time"
)

type Link struct {
	gorm.Model
	OriginalURL string `json:"original_url" gorm:"type:text;not null"`
	ShortCode   string `json:"short_code" gorm:"type:varchar(10);unique;not null"`
	Clicks      int    `json:"clicks" gorm:"default:0"`
}

type ClickOutbox struct {
	ID         uint      `gorm:"primaryKey"` // Unique ID for each outbox entry
	ShortCode  string    `gorm:"index"`      // Short code (e.g., "abc123") for the link
	ClickCount int       // Number of clicks (e.g., 1 per request)
	Processed  bool      // Whether the worker has processed this entry
	CreatedAt  time.Time // Timestamp of click event
}
