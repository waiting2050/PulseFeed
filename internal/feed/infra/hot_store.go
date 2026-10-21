package infra

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"pulsefeed/internal/feed/domain"
)

const (
	hotBucketPrefix = "feed:hot:minute:v1"
	hotWindowPrefix = "feed:hot:window:v1"
	hotBucketTTL    = 2 * time.Hour
	hotWindowTTL    = 30 * time.Second
)

// HotStore 用 Redis 分钟桶 ZSET 承载热榜：互动行为写入当前分钟桶，
// 查询时把最近一个窗口的桶合并成一个临时 ZSET 再取 TopN。
type HotStore struct {
	rdb *redis.Client
}

func NewHotStore(rdb *redis.Client) *HotStore {
	return &HotStore{rdb: rdb}
}

func (s *HotStore) AddScore(ctx context.Context, videoID int64, delta float64) error {
	if s.rdb == nil {
		return nil
	}

	key := hotBucketKey(time.Now().UTC())
	pipe := s.rdb.Pipeline()
	pipe.ZIncrBy(ctx, key, delta, strconv.FormatInt(videoID, 10))
	pipe.Expire(ctx, key, hotBucketTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *HotStore) TopScores(ctx context.Context, window time.Duration, limit int) ([]domain.ScoreEntry, error) {
	if s.rdb == nil || limit <= 0 {
		return nil, nil
	}

	now := time.Now().UTC()
	minutes := int(window / time.Minute)
	if minutes <= 0 {
		minutes = 1
	}

	keys := make([]string, 0, minutes)
	for i := 0; i < minutes; i++ {
		keys = append(keys, hotBucketKey(now.Add(-time.Duration(i)*time.Minute)))
	}

	windowKey := fmt.Sprintf("%s:%d", hotWindowPrefix, now.Unix()/60)
	if err := s.rdb.ZUnionStore(ctx, windowKey, &redis.ZStore{Keys: keys, Aggregate: "SUM"}).Err(); err != nil {
		return nil, err
	}
	_ = s.rdb.Expire(ctx, windowKey, hotWindowTTL).Err()

	raw, err := s.rdb.ZRevRangeWithScores(ctx, windowKey, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}

	entries := make([]domain.ScoreEntry, 0, len(raw))
	for _, item := range raw {
		id, err := strconv.ParseInt(fmt.Sprint(item.Member), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		entries = append(entries, domain.ScoreEntry{VideoID: id, Score: item.Score})
	}
	return entries, nil
}

func hotBucketKey(t time.Time) string {
	return fmt.Sprintf("%s:%s", hotBucketPrefix, t.Format("200601021504"))
}
