package domain

import (
	"context"
	"time"

	"pulsefeed/internal/shared/cursor"
)

// Repository 提供 Feed 卡片数据，返回条数由调用方控制。
type Repository interface {
	ListTimeline(ctx context.Context, page *cursor.Page, limit int) ([]*Item, error)
	ListByIDs(ctx context.Context, ids []int64) ([]*Item, error)
}

// HotStore 提供热榜读写，热榜分数由互动行为累加。
type HotStore interface {
	TopScores(ctx context.Context, window time.Duration, limit int) ([]ScoreEntry, error)
	AddScore(ctx context.Context, videoID int64, delta float64) error
}

// PageCache 缓存整页 Feed，Redis 不可用时由实现方直接返回未命中。
type PageCache interface {
	Get(ctx context.Context, key string) (*Page, bool)
	Set(ctx context.Context, key string, page *Page, ttl time.Duration)
}
