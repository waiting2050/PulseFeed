package domain

import (
	"context"

	feeddomain "pulsefeed/internal/feed/domain"
)

// RecallSource 是一路召回，返回候选视频 ID；单路失败由上层降级忽略。
type RecallSource interface {
	Recall(ctx context.Context, userID int64, limit int) ([]int64, error)
}

// CardRepository 按 ID 批量取视频卡片，复用 Feed 的组装结果。
type CardRepository interface {
	ListByIDs(ctx context.Context, ids []int64) ([]*feeddomain.Item, error)
}

// ExposedStore 记录用户已看过的视频，用于曝光去重。
type ExposedStore interface {
	Filter(ctx context.Context, userID int64, videoIDs []int64) ([]int64, error)
	Mark(ctx context.Context, userID int64, videoIDs []int64) error
}
