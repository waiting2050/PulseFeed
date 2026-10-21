package domain

import (
	"context"

	"pulsefeed/internal/shared/cursor"
)

type Repository interface {
	Create(ctx context.Context, video *Video, stat *Stat) error
	FindByID(ctx context.Context, id int64) (*Video, error)
	FindByIdempotencyKey(ctx context.Context, authorID int64, key string) (*Video, error)
	ListByAuthor(ctx context.Context, authorID int64, page *cursor.Page, limit int) ([]*Video, error)
}
