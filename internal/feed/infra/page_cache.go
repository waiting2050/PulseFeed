package infra

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"pulsefeed/internal/feed/domain"
)

// PageCache 用 Redis 缓存整页 Feed；Redis 未就绪时退化为永久未命中。
type PageCache struct {
	rdb    *redis.Client
	logger *slog.Logger
}

func NewPageCache(rdb *redis.Client, logger *slog.Logger) *PageCache {
	return &PageCache{rdb: rdb, logger: logger}
}

func (c *PageCache) Get(ctx context.Context, key string) (*domain.Page, bool) {
	if c.rdb == nil {
		return nil, false
	}

	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			c.logger.Warn("feed page cache get failed", "key", key, "error", err)
		}
		return nil, false
	}

	var page domain.Page
	if err := json.Unmarshal(raw, &page); err != nil {
		c.logger.Warn("feed page cache decode failed", "key", key, "error", err)
		return nil, false
	}
	return &page, true
}

func (c *PageCache) Set(ctx context.Context, key string, page *domain.Page, ttl time.Duration) {
	if c.rdb == nil {
		return
	}

	raw, err := json.Marshal(page)
	if err != nil {
		c.logger.Warn("feed page cache encode failed", "key", key, "error", err)
		return
	}
	if err := c.rdb.Set(ctx, key, raw, ttl).Err(); err != nil {
		c.logger.Warn("feed page cache set failed", "key", key, "error", err)
	}
}
