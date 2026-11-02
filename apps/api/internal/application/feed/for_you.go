// for_you.go 实现 for-you 场景策略：调用推荐服务取排序后的 clip，按 ID 批量水合作者条目。
package appfeed

import (
	"context"

	apprecommendation "pulsefeed/internal/application/recommendation"
	domfeed "pulsefeed/internal/domain/feed"
)

// Recommender 是推荐读取端口，由 application/recommendation.Service 实现。
type Recommender interface {
	// Recommend 返回一页推荐结果，Items 含 clip ID 与分数，NextCursor 携带 snapshot_at。
	Recommend(ctx context.Context, viewerID int64, cursor *domfeed.Cursor, limit int) (*apprecommendation.Result, error)
}

// ForYouStrategy 是 for-you 场景策略：推荐排序 + 卡片/计数批量水合。
// 该场景个性化且依赖实时行为，声明不缓存，避免页缓存把某个读者的排序结果复用给他人。
type ForYouStrategy struct {
	repo        domfeed.Repository
	recommender Recommender
}

// NewForYouStrategy 创建 for-you 场景策略。
func NewForYouStrategy(repo domfeed.Repository, recommender Recommender) *ForYouStrategy {
	return &ForYouStrategy{repo: repo, recommender: recommender}
}

// Scene 返回 for-you 场景标识。
func (s *ForYouStrategy) Scene() domfeed.Scene {
	return domfeed.SceneForYou
}

// Cacheable 报告 for-you 不使用页缓存。
func (s *ForYouStrategy) Cacheable() bool {
	return false
}

// Load 读取一页推荐条目：未登录返回 ErrViewerRequired，游标缺少 snapshot_at 返回 ErrInvalidCursor；
// 推荐结果按 ID 批量回源卡片与计数，已删除 clip 跳过，展示顺序保持推荐的创作者交错顺序。
func (s *ForYouStrategy) Load(ctx context.Context, req *FeedRequest, cursor *domfeed.Cursor, count int) (*Page, error) {
	if req.ViewerID <= 0 {
		return nil, domfeed.ErrViewerRequired
	}
	if cursor != nil && cursor.SnapshotAt == nil {
		return nil, domfeed.ErrInvalidCursor
	}

	limit := count - 1
	if limit < 1 {
		limit = 1
	}
	result, err := s.recommender.Recommend(ctx, req.ViewerID, cursor, limit)
	if err != nil {
		return nil, err
	}
	page := &Page{NextCursor: result.NextCursor, HasMore: result.HasMore}
	if len(result.Items) == 0 {
		return page, nil
	}

	ids := make([]int64, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ClipID)
	}
	cards, err := s.repo.BatchGetEntries(ctx, ids)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.BatchGetStats(ctx, ids)
	if err != nil {
		return nil, err
	}

	entries := make([]*domfeed.FeedEntry, 0, len(result.Items))
	for _, item := range result.Items {
		card, ok := cards[item.ClipID]
		if !ok {
			// clip 已删除或转非发布态：跳过，游标仍以推荐排序锚点为准，不影响后续翻页。
			continue
		}
		entry := *card
		entry.PublishedAt = item.PublishedAt
		if value, ok := stats[item.ClipID]; ok {
			entry.Stats = *value
		}
		entries = append(entries, &entry)
	}
	page.Entries = entries
	return page, nil
}
