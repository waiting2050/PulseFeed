package domain

import (
	"context"

	"pulsefeed/internal/shared/cursor"
)

// Repository 保存互动行为与评论，计数变更与行为写入需要保持同一事务。
type Repository interface {
	VideoExists(ctx context.Context, videoID int64) (bool, error)
	VideoAuthor(ctx context.Context, videoID int64) (int64, error)
	AddAction(ctx context.Context, userID, videoID int64, action int) (bool, error)
	RemoveAction(ctx context.Context, userID, videoID int64, action int) (bool, error)
	CreateComment(ctx context.Context, comment *Comment) error
	FindComment(ctx context.Context, id int64) (*Comment, error)
	DeleteComment(ctx context.Context, id int64) error
	ListComments(ctx context.Context, videoID int64, page *cursor.Page, limit int) ([]*Comment, error)
}

// HotStore 接收互动带来的热榜分数增量。
type HotStore interface {
	AddScore(ctx context.Context, videoID int64, delta float64) error
}
