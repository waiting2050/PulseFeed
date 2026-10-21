package domain

import (
	"context"
	"time"

	"pulsefeed/internal/shared/cursor"
)

// Repository 提供 Timeline 页数据，返回的条数由调用方控制。
type Repository interface {
	ListTimeline(ctx context.Context, page *cursor.Page, limit int) ([]*Item, error)
}

// PageCache 缓存整页 Feed，Redis 不可用时由实现方直接返回未命中。
type PageCache interface {
	Get(ctx context.Context, key string) (*Page, bool)
	Set(ctx context.Context, key string, page *Page, ttl time.Duration)
}
