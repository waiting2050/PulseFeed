// hot.go 实现 hot 场景策略：优先合并 Redis 最近 60 个分钟桶的热度增量排序，
// 缓存未启用/读失败/窗口为空时回源 DB（按 ScoreHot 等价 SQL 表达式排序），并记录 hot=fallback_db 日志。
package appfeed

import (
	"context"
	"log"

	appobservability "pulsefeed/internal/application/observability"
	domfeed "pulsefeed/internal/domain/feed"
)

// hotHydrateRounds 是单次热榜请求补取候选的最大轮数：水合会丢弃 DB 中已删除的条目，
// 首轮不足一页时继续向后取下一批候选，避免删条占满窗口导致分页提前结束。
// 每轮固定 2 次 DB 往返（BatchGetEntries + BatchGetStats），因此单请求最多
// 2*hotHydrateRounds=6 次 DB 往返（原实现固定 2 次，最多增加 4 次）；Redis 仍为 1 次 pipeline 往返。
const hotHydrateRounds = 3

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
	// metrics 记录热度窗口读取故障；未注入时静默跳过。
	metrics appobservability.Metrics
}

// NewHotStrategy 创建 hot 场景策略；cache 为 nil 时恒走 DB 回源。
func NewHotStrategy(repo domfeed.Repository, cache HotCache) *HotStrategy {
	return &HotStrategy{repo: repo, cache: cache}
}

// setMetrics 注入可观测性端口，用于记录热度窗口读取故障。
func (s *HotStrategy) setMetrics(metrics appobservability.Metrics) {
	s.metrics = metrics
}

// observeCache 记录一次缓存访问结果；未注入指标时静默跳过。
func (s *HotStrategy) observeCache(result string) {
	if s.metrics != nil {
		s.metrics.ObserveCache(result)
	}
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
		// 缓存层已区分故障与空窗口：故障一律回源 DB，并记 error 指标而不是 empty_window。
		log.Printf("feed hot=fallback_db reason=cache_error err=%v", err)
		s.observeCache("hot_error")
		return s.loadFromDB(ctx, cursor, count, "")
	}
	if len(items) == 0 {
		s.observeCache("hot_empty")
		return s.loadFromDB(ctx, cursor, count, "empty_window")
	}

	// 先取出游标之后的全部候选再分批水合：只取一页候选会被已删除条目挤掉名额，
	// 页内条数不足 count 时不生成游标，HasMore 变为 false，分页提前结束。
	candidates := hotCandidates(items, cursor)
	if len(candidates) == 0 {
		return newHotPage(nil, count), nil
	}
	entries, examined, err := s.backfill(ctx, candidates, count)
	if err != nil {
		return nil, err
	}
	page := newHotPage(entries, count)
	// 轮数上限触发短页但窗口内仍有未检查候选：用最后一个已检查候选锚定游标并显式声明
	// 还有下一页，保证「有内容就能翻到」。锚点取 candidates[examined-1] 而不是最后一条
	// 已水合条目：二者之间的候选都已确认被删除，下一次从锚点之后继续既不会跳条也不会重复。
	if len(entries) < count && examined < len(candidates) {
		anchor := candidates[examined-1]
		page.NextCursor = domfeed.EncodeCursor(&domfeed.Cursor{
			Score:  anchor.Score,
			ClipID: anchor.ClipID,
		})
		page.HasMore = true
	}
	return page, nil
}

// backfill 按 count 一批向后水合候选，直到凑满 count 条或触及 hotHydrateRounds 轮上限。
// 返回已水合条目与已检查的候选数；条目保持窗口的 (score, clip_id) 递减顺序，
// 因此按 count 截断与游标锚点仍满足严格递减语义，不跳条也不重复。
func (s *HotStrategy) backfill(ctx context.Context, candidates []domfeed.HotItem, count int) ([]*domfeed.FeedEntry, int, error) {
	entries := make([]*domfeed.FeedEntry, 0, count)
	examined := 0
	for round := 0; round < hotHydrateRounds && len(entries) < count && examined < len(candidates); round++ {
		end := examined + count
		if end > len(candidates) {
			end = len(candidates)
		}
		batch, err := s.hydrate(ctx, candidates[examined:end])
		if err != nil {
			return nil, examined, err
		}
		examined = end
		entries = append(entries, batch...)
	}
	if len(entries) > count {
		entries = entries[:count]
	}
	return entries, examined, nil
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

// hotCandidates 按 (score, clip_id) 严格递减游标从已排序窗口取出游标之后的全部候选，
// 供补取时按 count 分批水合。
func hotCandidates(items []domfeed.HotItem, cursor *domfeed.Cursor) []domfeed.HotItem {
	result := make([]domfeed.HotItem, 0, len(items))
	for _, item := range items {
		if cursor != nil {
			after := item.Score < cursor.Score ||
				(item.Score == cursor.Score && item.ClipID < cursor.ClipID)
			if !after {
				continue
			}
		}
		result = append(result, item)
	}
	return result
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
