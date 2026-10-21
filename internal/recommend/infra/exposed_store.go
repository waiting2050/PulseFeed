package infra

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	exposedKeyPrefix = "recommend:exposed:v1"
	exposedTTL       = 24 * time.Hour
)

// ExposedStore 用 Redis SET 记录用户近期已曝光的视频。
type ExposedStore struct {
	rdb *redis.Client
}

func NewExposedStore(rdb *redis.Client) *ExposedStore {
	return &ExposedStore{rdb: rdb}
}

func (s *ExposedStore) Filter(ctx context.Context, userID int64, videoIDs []int64) ([]int64, error) {
	if s.rdb == nil || len(videoIDs) == 0 {
		return videoIDs, nil
	}

	key := exposedKey(userID)
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.BoolCmd, len(videoIDs))
	for i, id := range videoIDs {
		cmds[i] = pipe.SIsMember(ctx, key, strconv.FormatInt(id, 10))
	}
	// Redis 不可用时不做曝光过滤，保持推荐可用。
	if _, err := pipe.Exec(ctx); err != nil {
		return videoIDs, nil
	}

	kept := make([]int64, 0, len(videoIDs))
	for i, cmd := range cmds {
		if !cmd.Val() {
			kept = append(kept, videoIDs[i])
		}
	}
	return kept, nil
}

func (s *ExposedStore) Mark(ctx context.Context, userID int64, videoIDs []int64) error {
	if s.rdb == nil || len(videoIDs) == 0 {
		return nil
	}

	key := exposedKey(userID)
	members := make([]interface{}, 0, len(videoIDs))
	for _, id := range videoIDs {
		members = append(members, strconv.FormatInt(id, 10))
	}

	pipe := s.rdb.Pipeline()
	pipe.SAdd(ctx, key, members...)
	pipe.Expire(ctx, key, exposedTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func exposedKey(userID int64) string {
	return fmt.Sprintf("%s:%d", exposedKeyPrefix, userID)
}
