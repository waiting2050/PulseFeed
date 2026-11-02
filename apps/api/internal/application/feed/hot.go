// hot.go 实现 hot 场景策略：优先合并 Redis 最近 60 个分钟桶的热度增量排序，
// 缓存未启用/读失败/窗口为空时回源 DB（按 ScoreHot 等价 SQL 表达式排序），并记录 hot=fallback_db 日志。
package appfeed

import (
	"context"
	"log"

	domfeed "pulsefeed/internal/domain/feed"
)

// HotCache 描述 hot 场景所需的缓存能力，由 infra/cache 的 FeedCache 实现。
type HotCache interface {
	// Enabled 报告缓存是否启用。
	Enabled() bool
	// ListHotWindow 返回最近 60 个分钟桶合并后按热度递减排序的条目。
	ListHotWindow(ctx context.Context) ([]domfeed.HotItem, error)
}

// HotStrategy 是 hot 场景策略：Redis 分钟桶驱动排序，缺失时 DB 回源。
// 该场景不使用页缓存：页缓存只存条目、不存 hot 游标，命中后无法正确续翻。
type HotStrategy struct {
	repo  domfeed.Repository
	cache HotCache
}

// NewHotStrategy 创建 hot 场景策略；cache 为 nil 时恒走 DB 回源。
func NewHotStrategy(repo domfeed.Repository, cache HotCache) *HotStrategy {
	return &HotStrategy{repo: repo, cache: cache}
}

// Scene 返回 hot 场景标识。
func (s *HotStrategy) Scene() domfeed.Scene {
	return domfeed.SceneHot
}

// Cacheable 报告 hot 场景不使用页缓存。
func (s *HotStrategy) Cacheable() bool {
	return false
}

// Load 读取一页热榜：优先用 Redis 分钟桶窗口，条目经卡片/计数批量水合后按 (score, clip_id) 游标截取；
// 缓存不可用或窗口为空时回源 DB。
func (s *HotStrategy) Load(ctx context.Context, _ *FeedRequest, cursor *domfeed.Cursor, count int) (*Page, error) {
	if s.cache == nil || !s.cache.Enabled() {
		return s.loadFromDB(ctx, cursor, count, "cache_disabled")
	}

	items, err := s.cache.ListHotWindow(ctx)
	if err != nil {
		log.Printf("feed hot=fallback_db reason=cache_error err=%v", err)
		return s.loadFromDB(ctx, cursor, count, "")
	}
	if len(items) == 0 {
		return s.loadFromDB(ctx, cursor, count, "empty_window")
	}

	chosen := paginateHot(items, cursor, count)
	entries, err := s.hydrate(ctx, chosen)
	if err != nil {
		return nil, err
	}
	return newHotPage(entries, count), nil
}

// loadFromDB 回源 DB 读取一页热榜；reason 非空时统一打印 hot=fallback_db 日志。
func (s *HotStrategy) loadFromDB(ctx context.Context, cursor *domfeed.Cursor, count int, reason string) (*Page, error) {
	if reason != "" {
		log.Printf("feed hot=fallback_db reason=%s", reason)
	}
	entries, err := s.repo.ListHotPage(ctx, cursor, count)
	if err != nil {
		return nil, err
	}
	return newHotPage(entries, count), nil
}

// paginateHot 按 (score, clip_id) 严格递减游标从已排序窗口截取至多 count 条。
func paginateHot(items []domfeed.HotItem, cursor *domfeed.Cursor, count int) []domfeed.HotItem {
	page := make([]domfeed.HotItem, 0, count)
	for _, item := range items {
		if cursor != nil {
			after := item.Score < cursor.Score ||
				(item.Score == cursor.Score && item.ClipID < cursor.ClipID)
			if !after {
				continue
			}
		}
		page = append(page, item)
		if len(page) == count {
			break
		}
	}
	return page
}

// hydrate 把热度窗口条目水合成完整 Feed 条目：按 ID 批量回源卡片与计数，已删除 clip 跳过；
// 热度分写回条目 Score，供游标锚点使用。
func (s *HotStrategy) hydrate(ctx context.Context, items []domfeed.HotItem) ([]*domfeed.FeedEntry, error) {
	if len(items) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
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

	entries := make([]*domfeed.FeedEntry, 0, len(items))
	for _, item := range items {
		card, ok := cards[item.ClipID]
		if !ok {
			continue
		}
		entry := *card
		entry.Score = item.Score
		if value, ok := stats[item.ClipID]; ok {
			entry.Stats = *value
		}
		entries = append(entries, &entry)
	}
	return entries, nil
}

// newHotPage 组装一页热榜结果：count 为页大小 +1，多出的一条用于判定 has_more；
// NextCursor 取本次将返回的最后一条（索引 count-2）的 (score, clip_id)。
func newHotPage(entries []*domfeed.FeedEntry, count int) *Page {
	page := &Page{Entries: entries}
	if len(entries) >= count && count >= 2 {
		anchor := entries[count-2]
		page.NextCursor = domfeed.EncodeCursor(&domfeed.Cursor{
			Score:  anchor.Score,
			ClipID: anchor.ClipID,
		})
	}
	return page
}
