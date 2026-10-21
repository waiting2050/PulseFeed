package infra

import (
	"context"
	"time"

	"gorm.io/gorm"

	"pulsefeed/internal/feed/domain"
	"pulsefeed/internal/shared/cursor"
	videodomain "pulsefeed/internal/video/domain"
)

type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

type timelineRow struct {
	VideoID       int64
	AuthorID      int64
	Title         string
	MediaURL      string
	CoverURL      string
	PublishedAt   time.Time
	LikeCount     int64
	CommentCount  int64
	FavoriteCount int64
}

func (r *GormRepository) ListTimeline(ctx context.Context, page *cursor.Page, limit int) ([]*domain.Item, error) {
	query := r.db.WithContext(ctx).
		Table("videos AS v").
		Select(
			"v.id AS video_id, v.author_id, v.title, v.media_url, v.cover_url, v.published_at, "+
				"COALESCE(st.like_count, 0) AS like_count, "+
				"COALESCE(st.comment_count, 0) AS comment_count, "+
				"COALESCE(st.favorite_count, 0) AS favorite_count",
		).
		Joins("LEFT JOIN video_stats AS st ON st.video_id = v.id").
		Where("v.status = ?", videodomain.StatusPublished)

	if page != nil {
		query = query.Where(
			"(v.published_at < ? OR (v.published_at = ? AND v.id < ?))",
			page.PublishedAt, page.PublishedAt, page.ID,
		)
	}

	var rows []timelineRow
	if err := query.Order("v.published_at DESC, v.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}

	items := make([]*domain.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, &domain.Item{
			VideoID:       row.VideoID,
			AuthorID:      row.AuthorID,
			Title:         row.Title,
			MediaURL:      row.MediaURL,
			CoverURL:      row.CoverURL,
			LikeCount:     row.LikeCount,
			CommentCount:  row.CommentCount,
			FavoriteCount: row.FavoriteCount,
			PublishedAt:   row.PublishedAt,
		})
	}
	return items, nil
}
