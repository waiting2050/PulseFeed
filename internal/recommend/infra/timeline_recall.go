package infra

import (
	"context"

	feeddomain "pulsefeed/internal/feed/domain"
)

// TimelineRecall 作为兜底召回，保证冷启动和新用户也有内容可推。
type TimelineRecall struct {
	repo feeddomain.Repository
}

func NewTimelineRecall(repo feeddomain.Repository) *TimelineRecall {
	return &TimelineRecall{repo: repo}
}

func (r *TimelineRecall) Recall(ctx context.Context, _ int64, limit int) ([]int64, error) {
	if r.repo == nil || limit <= 0 {
		return nil, nil
	}

	items, err := r.repo.ListTimeline(ctx, nil, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.VideoID)
	}
	return ids, nil
}
