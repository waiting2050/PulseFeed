package domain

import (
	"context"
	"errors"
	"time"

	"pulsefeed/internal/shared/cursor"
)

var ErrInvalidCursor = errors.New("游标不合法")

type Notification struct {
	ID            int64
	UserID        int64
	ActorID       int64
	ActorNickname string
	ActorAvatar   string
	Type          string
	VideoID       int64
	CommentID     int64
	Content       string
	IsRead        bool
	CreatedAt     time.Time
}

type Repository interface {
	Create(ctx context.Context, notification *Notification) error
	List(ctx context.Context, userID int64, page *cursor.Page, limit int) ([]*Notification, error)
	UnreadCount(ctx context.Context, userID int64) (int64, error)
	MarkAllRead(ctx context.Context, userID int64) error
}
