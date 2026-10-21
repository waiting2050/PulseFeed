package infra

import (
	"context"
	"time"

	feeddomain "pulsefeed/internal/feed/domain"
)

const hotRecallWindow = 60 * time.Minute

// HotRecall 把热榜作为第一路召回，互动越多的视频优先级越高。
type HotRecall struct {
	hot feeddomain.HotStore
}

func NewHotRecall(hot feeddomain.HotStore) *HotRecall {
	return &HotRecall{hot: hot}
}

func (r *HotRecall) Recall(ctx context.Context, _ int64, limit int) ([]int64, error) {
	if r.hot == nil || limit <= 0 {
		return nil, nil
	}

	entries, err := r.hot.TopScores(ctx, hotRecallWindow, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.VideoID)
	}
	return ids, nil
}
