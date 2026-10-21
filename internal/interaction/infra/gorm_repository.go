package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"pulsefeed/internal/interaction/domain"
	"pulsefeed/internal/shared/cursor"
	videodomain "pulsefeed/internal/video/domain"
)

type ActionModel struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	UserID    int64     `gorm:"not null;uniqueIndex:uk_user_video_action,priority:1"`
	VideoID   int64     `gorm:"not null;uniqueIndex:uk_user_video_action,priority:2"`
	Action    int8      `gorm:"not null;uniqueIndex:uk_user_video_action,priority:3"`
	CreatedAt time.Time `gorm:"not null"`
}

func (ActionModel) TableName() string {
	return "interaction_actions"
}

type CommentModel struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	VideoID   int64     `gorm:"not null;index:idx_comments_video_created,priority:1"`
	UserID    int64     `gorm:"not null"`
	Content   string    `gorm:"size:500;not null"`
	CreatedAt time.Time `gorm:"not null;index:idx_comments_video_created,priority:2"`
}

func (CommentModel) TableName() string {
	return "comments"
}

type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

func (r *GormRepository) VideoExists(ctx context.Context, videoID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Table("videos").
		Where("id = ? AND status = ?", videoID, videodomain.StatusPublished).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *GormRepository) VideoAuthor(ctx context.Context, videoID int64) (int64, error) {
	var ids []int64
	err := r.db.WithContext(ctx).
		Table("videos").
		Where("id = ? AND status = ?", videoID, videodomain.StatusPublished).
		Limit(1).
		Pluck("author_id", &ids).Error
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, domain.ErrVideoNotFound
	}
	return ids[0], nil
}

func (r *GormRepository) AddAction(ctx context.Context, userID, videoID int64, action int) (bool, error) {
	column, err := statColumn(action)
	if err != nil {
		return false, err
	}

	created := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := &ActionModel{UserID: userID, VideoID: videoID, Action: int8(action)}
		if err := tx.Create(model).Error; err != nil {
			var mysqlErr *gomysql.MySQLError
			if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
				return nil
			}
			return err
		}
		created = true
		return bumpStat(tx, videoID, column, 1)
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

func (r *GormRepository) RemoveAction(ctx context.Context, userID, videoID int64, action int) (bool, error) {
	column, err := statColumn(action)
	if err != nil {
		return false, err
	}

	removed := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.
			Where("user_id = ? AND video_id = ? AND action = ?", userID, videoID, action).
			Delete(&ActionModel{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		removed = true
		return bumpStat(tx, videoID, column, -1)
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

func (r *GormRepository) CreateComment(ctx context.Context, comment *domain.Comment) error {
	model := &CommentModel{
		VideoID:   comment.VideoID,
		UserID:    comment.UserID,
		Content:   comment.Content,
		CreatedAt: comment.CreatedAt,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(model).Error; err != nil {
			return err
		}
		return bumpStat(tx, comment.VideoID, "comment_count", 1)
	})
	if err != nil {
		return err
	}

	comment.ID = model.ID
	comment.CreatedAt = model.CreatedAt
	return nil
}

func (r *GormRepository) FindComment(ctx context.Context, id int64) (*domain.Comment, error) {
	var model CommentModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrCommentNotFound
	}
	if err != nil {
		return nil, err
	}
	return toCommentDomain(&model), nil
}

func (r *GormRepository) DeleteComment(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model CommentModel
		err := tx.First(&model, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrCommentNotFound
		}
		if err != nil {
			return err
		}

		if err := tx.Delete(&CommentModel{}, id).Error; err != nil {
			return err
		}
		return bumpStat(tx, model.VideoID, "comment_count", -1)
	})
}

func (r *GormRepository) ListComments(ctx context.Context, videoID int64, page *cursor.Page, limit int) ([]*domain.Comment, error) {
	query := r.db.WithContext(ctx).Model(&CommentModel{}).Where("video_id = ?", videoID)
	if page != nil {
		query = query.Where(
			"(created_at < ? OR (created_at = ? AND id < ?))",
			page.PublishedAt, page.PublishedAt, page.ID,
		)
	}

	var models []CommentModel
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&models).Error; err != nil {
		return nil, err
	}

	comments := make([]*domain.Comment, 0, len(models))
	for i := range models {
		comments = append(comments, toCommentDomain(&models[i]))
	}
	return comments, nil
}

// bumpStat 用单条 UPDATE 完成自增自减，避免先读后写导致并发覆盖。
func bumpStat(tx *gorm.DB, videoID int64, column string, delta int) error {
	sql := fmt.Sprintf(
		"UPDATE video_stats SET %s = GREATEST(%s + ?, 0), updated_at = ? WHERE video_id = ?",
		column, column,
	)
	return tx.Exec(sql, delta, time.Now().UTC(), videoID).Error
}

func statColumn(action int) (string, error) {
	switch action {
	case domain.ActionLike:
		return "like_count", nil
	case domain.ActionFavorite:
		return "favorite_count", nil
	default:
		return "", domain.ErrInvalidAction
	}
}

func toCommentDomain(model *CommentModel) *domain.Comment {
	return &domain.Comment{
		ID:        model.ID,
		VideoID:   model.VideoID,
		UserID:    model.UserID,
		Content:   model.Content,
		CreatedAt: model.CreatedAt,
	}
}
