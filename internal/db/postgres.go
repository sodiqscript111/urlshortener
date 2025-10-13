package db

import (
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"urlshorter/models"
)

func ConnectDB() (*gorm.DB, error) {
	dsn := "host=localhost user=postgres password=password dbname=testing sslmode=disable"
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := database.AutoMigrate(&models.Link{}, &models.ClickOutbox{}); err != nil {
		return nil, err
	}
	return database, nil
}
