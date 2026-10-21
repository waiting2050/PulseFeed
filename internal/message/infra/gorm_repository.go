package infra

import (
	"context"
	"time"

	"gorm.io/gorm"

	"pulsefeed/internal/message/domain"
	"pulsefeed/internal/shared/cursor"
)

type NotificationModel struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	UserID    int64  `gorm:"not null;index:idx_user_read_created,priority:1"`
	ActorID   int64  `gorm:"not null"`
	Type      string `gorm:"size:20;not null"`
	VideoID   *int64 `gorm:"index"`
	CommentID *int64
	Content   string    `gorm:"size:200;not null;default:''"`
	IsRead    bool      `gorm:"not null;default:false;index:idx_user_read_created,priority:2"`
	CreatedAt time.Time `gorm:"not null;index:idx_user_read_created,priority:3"`
}

func (NotificationModel) TableName() string {
	return "notifications"
}

type notificationRow struct {
	ID            int64
	UserID        int64
	ActorID       int64
	ActorNickname string
	ActorAvatar   string
	Type          string
	VideoID       *int64
	CommentID     *int64
	Content       string
	IsRead        bool
	CreatedAt     time.Time
}

type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

func (r *GormRepository) Create(ctx context.Context, notification *domain.Notification) error {
	model := &NotificationModel{
		UserID:    notification.UserID,
		ActorID:   notification.ActorID,
		Type:      notification.Type,
		Content:   notification.Content,
		IsRead:    notification.IsRead,
		CreatedAt: notification.CreatedAt,
	}
	if notification.VideoID > 0 {
		videoID := notification.VideoID
		model.VideoID = &videoID
	}
	if notification.CommentID > 0 {
		commentID := notification.CommentID
		model.CommentID = &commentID
	}

	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return err
	}
	notification.ID = model.ID
	return nil
}

func (r *GormRepository) List(ctx context.Context, userID int64, page *cursor.Page, limit int) ([]*domain.Notification, error) {
	query := r.db.WithContext(ctx).
		Table("notifications AS n").
		Select("n.id, n.user_id, n.actor_id, n.type, n.video_id, n.comment_id, n.content, n.is_read, n.created_at, "+
			"COALESCE(u.nickname, '') AS actor_nickname, COALESCE(u.avatar_url, '') AS actor_avatar").
		Joins("LEFT JOIN users AS u ON u.id = n.actor_id").
		Where("n.user_id = ?", userID)

	if page != nil {
		query = query.Where(
			"(n.created_at < ? OR (n.created_at = ? AND n.id < ?))",
			page.PublishedAt, page.PublishedAt, page.ID,
		)
	}

	var rows []notificationRow
	if err := query.Order("n.created_at DESC, n.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}

	notifications := make([]*domain.Notification, 0, len(rows))
	for i := range rows {
		notifications = append(notifications, toDomain(&rows[i]))
	}
	return notifications, nil
}

func (r *GormRepository) UnreadCount(ctx context.Context, userID int64) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&NotificationModel{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *GormRepository) MarkAllRead(ctx context.Context, userID int64) error {
	return r.db.WithContext(ctx).
		Model(&NotificationModel{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Update("is_read", true).Error
}

func toDomain(row *notificationRow) *domain.Notification {
	notification := &domain.Notification{
		ID:            row.ID,
		UserID:        row.UserID,
		ActorID:       row.ActorID,
		ActorNickname: row.ActorNickname,
		ActorAvatar:   row.ActorAvatar,
		Type:          row.Type,
		Content:       row.Content,
		IsRead:        row.IsRead,
		CreatedAt:     row.CreatedAt,
	}
	if row.VideoID != nil {
		notification.VideoID = *row.VideoID
	}
	if row.CommentID != nil {
		notification.CommentID = *row.CommentID
	}
	return notification
}
