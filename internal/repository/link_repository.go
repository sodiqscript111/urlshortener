package repository

import (
	"context"

	"gorm.io/gorm"
	"urlshorter/models"
)

type LinkRepository interface {
	Create(ctx context.Context, link *models.Link) error
	FindByCode(ctx context.Context, code string) (*models.Link, error)
	IncrementClicks(ctx context.Context, code string, count int) error
}

type postgresLinkRepository struct {
	db *gorm.DB
}

func NewPostgresLinkRepository(db *gorm.DB) LinkRepository {
	return &postgresLinkRepository{db: db}
}

func (r *postgresLinkRepository) Create(ctx context.Context, link *models.Link) error {
	return r.db.WithContext(ctx).Create(link).Error
}

func (r *postgresLinkRepository) FindByCode(ctx context.Context, code string) (*models.Link, error) {
	var link models.Link
	err := r.db.WithContext(ctx).Where("short_code = ?", code).First(&link).Error
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (r *postgresLinkRepository) IncrementClicks(ctx context.Context, code string, count int) error {
	return r.db.WithContext(ctx).Model(&models.Link{}).Where("short_code = ?", code).
		Update("clicks", gorm.Expr("clicks + ?", count)).Error
}
