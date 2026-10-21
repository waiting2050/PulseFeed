package infra

import (
	"context"
	"errors"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"pulsefeed/internal/shared/cursor"
	"pulsefeed/internal/video/domain"
)

type VideoModel struct {
	ID             int64     `gorm:"primaryKey;autoIncrement"`
	AuthorID       int64     `gorm:"not null;index;uniqueIndex:uk_author_idempotency,priority:1"`
	Title          string    `gorm:"size:100;not null"`
	MediaURL       string    `gorm:"size:255;not null"`
	CoverURL       string    `gorm:"size:255;not null;default:''"`
	Status         int8      `gorm:"not null;default:1;index"`
	IdempotencyKey *string   `gorm:"size:64;uniqueIndex:uk_author_idempotency,priority:2"`
	PublishedAt    time.Time `gorm:"not null;index"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

func (VideoModel) TableName() string {
	return "videos"
}

type VideoStatModel struct {
	VideoID       int64 `gorm:"primaryKey"`
	LikeCount     int64 `gorm:"not null;default:0"`
	CommentCount  int64 `gorm:"not null;default:0"`
	FavoriteCount int64 `gorm:"not null;default:0"`
	UpdatedAt     time.Time
}

func (VideoStatModel) TableName() string {
	return "video_stats"
}

type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

func (r *GormRepository) Create(ctx context.Context, video *domain.Video, stat *domain.Stat) error {
	var key *string
	if video.IdempotencyKey != "" {
		key = &video.IdempotencyKey
	}

	model := &VideoModel{
		AuthorID:       video.AuthorID,
		Title:          video.Title,
		MediaURL:       video.MediaURL,
		CoverURL:       video.CoverURL,
		Status:         int8(video.Status),
		IdempotencyKey: key,
		PublishedAt:    video.PublishedAt,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(model).Error; err != nil {
			return err
		}
		statModel := &VideoStatModel{
			VideoID:       model.ID,
			LikeCount:     stat.LikeCount,
			CommentCount:  stat.CommentCount,
			FavoriteCount: stat.FavoriteCount,
		}
		return tx.Create(statModel).Error
	})
	if err != nil {
		var mysqlErr *gomysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return domain.ErrIdempotencyConflict
		}
		return err
	}

	toDomain(model, video)
	stat.VideoID = model.ID
	return nil
}

func (r *GormRepository) FindByID(ctx context.Context, id int64) (*domain.Video, error) {
	var model VideoModel
	err := r.db.WithContext(ctx).
		Where("id = ? AND status = ?", id, domain.StatusPublished).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrVideoNotFound
	}
	if err != nil {
		return nil, err
	}
	return toDomain(&model, nil), nil
}

func (r *GormRepository) FindByIdempotencyKey(ctx context.Context, authorID int64, key string) (*domain.Video, error) {
	if key == "" {
		return nil, domain.ErrVideoNotFound
	}

	var model VideoModel
	err := r.db.WithContext(ctx).
		Where("author_id = ? AND idempotency_key = ? AND status = ?", authorID, key, domain.StatusPublished).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrVideoNotFound
	}
	if err != nil {
		return nil, err
	}
	return toDomain(&model, nil), nil
}

func (r *GormRepository) ListByAuthor(ctx context.Context, authorID int64, page *cursor.Page, limit int) ([]*domain.Video, error) {
	query := r.db.WithContext(ctx).
		Model(&VideoModel{}).
		Where("author_id = ? AND status = ?", authorID, domain.StatusPublished)

	if page != nil {
		query = query.Where(
			"(published_at < ? OR (published_at = ? AND id < ?))",
			page.PublishedAt, page.PublishedAt, page.ID,
		)
	}

	var models []VideoModel
	if err := query.Order("published_at DESC, id DESC").Limit(limit).Find(&models).Error; err != nil {
		return nil, err
	}

	videos := make([]*domain.Video, 0, len(models))
	for i := range models {
		videos = append(videos, toDomain(&models[i], nil))
	}
	return videos, nil
}

func toDomain(model *VideoModel, target *domain.Video) *domain.Video {
	if target == nil {
		target = &domain.Video{}
	}
	target.ID = model.ID
	target.AuthorID = model.AuthorID
	target.Title = model.Title
	target.MediaURL = model.MediaURL
	target.CoverURL = model.CoverURL
	target.Status = int(model.Status)
	if model.IdempotencyKey != nil {
		target.IdempotencyKey = *model.IdempotencyKey
	}
	target.PublishedAt = model.PublishedAt
	target.CreatedAt = model.CreatedAt
	target.UpdatedAt = model.UpdatedAt
	return target
}
