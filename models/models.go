package models

import "gorm.io/gorm"

type Link struct {
	gorm.Model
	OriginalURL string `json:"original_url" gorm:"type:text;not null"`
	ShortCode   string `json:"short_code" gorm:"type:varchar(10);unique;not null"`
	Clicks      int    `json:"clicks" gorm:"default:0"`
}
