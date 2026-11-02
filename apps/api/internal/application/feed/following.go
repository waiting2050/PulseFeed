// following.go 实现 following 场景策略：合并读者 inbox 与大 V 创作者 outbox，
// 按实时关注集过滤（取关即消失，无需清除历史 inbox），Redis 不可用时回源 DB 联查分页。
package appfeed

import (
	"context"
	"sort"

	domfeed "pulsefeed/internal/domain/feed"
)

// BigVFollowerThreshold 是大 V 粉丝数阈值：达到该值的创作者走拉模型（读时合并其 outbox），
// 低于该值的创作者走推模型（关注回填/发布扇出时写入读者 inbox）。
const BigVFollowerThreshold = 10000

// FollowingCache 描述 following 场景所需的缓存能力，由 infra/cache 的 FeedCache 实现。
type FollowingCache interface {
	// Enabled 报告缓存是否启用。
	Enabled() bool
	// AddInboxEntries 把条目推入读者 inbox（关注回填/发布扇出）。
	AddInboxEntries(ctx context.Context, userID int64, entries []domfeed.InboxEntry) error
	// ListInbox 读取读者 inbox 全部条目（≤裁剪上限）。
	ListInbox(ctx context.Context, userID int64) ([]domfeed.InboxEntry, error)
	// AddOutboxEntries 把条目推入创作者 outbox（大 V 拉模型）。
	AddOutboxEntries(ctx context.Context, creatorID int64, entries []domfeed.InboxEntry) error
	// ListOutbox 读取创作者 outbox 全部条目（≤裁剪上限）。
	ListOutbox(ctx context.Context, creatorID int64) ([]domfeed.InboxEntry, error)
	// GetCards 批量读取卡片缓存，未命中 ID 由调用方回源。
	GetCards(ctx context.Context, clipIDs []int64) map[int64]*domfeed.FeedEntry
	// SetCards 批量回填卡片缓存。
	SetCards(ctx context.Context, entries []*domfeed.FeedEntry)
	// GetStats 批量读取计数缓存，未命中 ID 由调用方回源。
	GetStats(ctx context.Context, clipIDs []int64) map[int64]*domfeed.EntryStats
	// SetStats 批量回填计数缓存。
	SetStats(ctx context.Context, stats map[int64]*domfeed.EntryStats)
}

// FollowingStrategy 是 following 场景策略：
// 读路径合并读者 inbox 与关注中大 V 的 outbox，按实时关注集过滤、排序去重后游标截取，
// 再经卡片/计数两级缓存水合；缓存任一步失败回源 DB（ListFollowingPage）。
type FollowingStrategy struct {
	repo  domfeed.Repository
	cache FollowingCache
}

// NewFollowingStrategy 创建 following 场景策略；cache 为 nil 时恒走 DB 回源。
func NewFollowingStrategy(repo domfeed.Repository, cache FollowingCache) *FollowingStrategy {
	return &FollowingStrategy{repo: repo, cache: cache}
}

// Scene 返回 following 场景标识。
func (s *FollowingStrategy) Scene() domfeed.Scene {
	return domfeed.SceneFollowing
}

// Cacheable 报告该场景不使用页缓存：关注/取关需立即生效，页缓存短 TTL 也会造成短暂不一致。
func (s *FollowingStrategy) Cacheable() bool {
	return false
}

// Load 读取一页 following 条目：未登录读者返回 ErrViewerRequired；缓存不可用时回源 DB。
func (s *FollowingStrategy) Load(ctx context.Context, req *FeedRequest, cursor *domfeed.Cursor, count int) (*Page, error) {
	if req.ViewerID <= 0 {
		return nil, domfeed.ErrViewerRequired
	}
	if s.cache == nil || !s.cache.Enabled() {
		entries, err := s.repo.ListFollowingPage(ctx, req.ViewerID, cursor, count)
		if err != nil {
			return nil, err
		}
		return &Page{Entries: entries}, nil
	}

	merged, err := s.loadMergedInbox(ctx, req.ViewerID)
	if err != nil {
		// 缓存读失败回源 DB 联查，保证关注流可用性。
		entries, fallbackErr := s.repo.ListFollowingPage(ctx, req.ViewerID, cursor, count)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		return &Page{Entries: entries}, nil
	}

	page := paginateInbox(merged, cursor, count)
	entries, err := s.hydrate(ctx, page)
	if err != nil {
		return nil, err
	}
	return &Page{Entries: entries}, nil
}

// loadMergedInbox 读取读者 inbox 与关注中大 V 的 outbox 并合并：
// inbox 条目按实时关注集过滤（取关即消失），合并后按 (published_at, clip_id) 严格递减排序去重；
// 关注集或任一缓存读取失败时返回错误，由调用方回源 DB。
func (s *FollowingStrategy) loadMergedInbox(ctx context.Context, viewerID int64) ([]domfeed.InboxEntry, error) {
	creators, err := s.repo.ListFollowedCreators(ctx, viewerID)
	if err != nil {
		return nil, err
	}
	if len(creators) == 0 {
		return nil, nil
	}

	inbox, err := s.cache.ListInbox(ctx, viewerID)
	if err != nil {
		return nil, err
	}

	followed := make(map[int64]bool, len(creators))
	bigVs := make([]int64, 0, len(creators))
	for _, creator := range creators {
		followed[creator.CreatorID] = true
		if creator.FollowerCount >= BigVFollowerThreshold {
			bigVs = append(bigVs, creator.CreatorID)
		}
	}

	merged := make([]domfeed.InboxEntry, 0, len(inbox))
	for _, entry := range inbox {
		if followed[entry.CreatorID] {
			merged = append(merged, entry)
		}
	}
	for _, creatorID := range bigVs {
		outbox, err := s.cache.ListOutbox(ctx, creatorID)
		if err != nil {
			return nil, err
		}
		merged = append(merged, outbox...)
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].PublishedAt.Equal(merged[j].PublishedAt) {
			return merged[i].ClipID > merged[j].ClipID
		}
		return merged[i].PublishedAt.After(merged[j].PublishedAt)
	})

	// 同一 clip 可能同时存在于 inbox 与 outbox，排序后按 clip_id 去重保留先出现者。
	deduped := merged[:0]
	seen := make(map[int64]bool, len(merged))
	for _, entry := range merged {
		if seen[entry.ClipID] {
			continue
		}
		seen[entry.ClipID] = true
		deduped = append(deduped, entry)
	}
	return deduped, nil
}

// paginateInbox 按复合游标从已排序条目截取至多 count 条。
func paginateInbox(entries []domfeed.InboxEntry, cursor *domfeed.Cursor, count int) []domfeed.InboxEntry {
	page := make([]domfeed.InboxEntry, 0, count)
	for _, entry := range entries {
		if cursor != nil {
			after := entry.PublishedAt.Before(cursor.PublishedAt) ||
				(entry.PublishedAt.Equal(cursor.PublishedAt) && entry.ClipID < cursor.ClipID)
			if !after {
				continue
			}
		}
		page = append(page, entry)
		if len(page) == count {
			break
		}
	}
	return page
}

// hydrate 把轻量 inbox 条目水合成完整 Feed 条目：卡片与计数先读缓存，未命中批量回源并回填；
// 卡片缺失（clip 已删除或不可见）的条目跳过。
func (s *FollowingStrategy) hydrate(ctx context.Context, page []domfeed.InboxEntry) ([]*domfeed.FeedEntry, error) {
	if len(page) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(page))
	for _, entry := range page {
		ids = append(ids, entry.ClipID)
	}

	cards := s.cache.GetCards(ctx, ids)
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := cards[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		loaded, err := s.repo.BatchGetEntries(ctx, missing)
		if err != nil {
			return nil, err
		}
		// 卡片缓存只存视图字段，计数在水合时挂接，避免计数变化导致卡片缓存频繁失效。
		fresh := make([]*domfeed.FeedEntry, 0, len(loaded))
		for _, entry := range loaded {
			cards[entry.ClipID] = entry
			fresh = append(fresh, entry)
		}
		s.cache.SetCards(ctx, fresh)
	}

	stats := s.cache.GetStats(ctx, ids)
	missing = missing[:0]
	for _, id := range ids {
		if _, ok := stats[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		loaded, err := s.repo.BatchGetStats(ctx, missing)
		if err != nil {
			return nil, err
		}
		for id, value := range loaded {
			stats[id] = value
		}
		s.cache.SetStats(ctx, loaded)
	}

	entries := make([]*domfeed.FeedEntry, 0, len(page))
	for _, inboxEntry := range page {
		card, ok := cards[inboxEntry.ClipID]
		if !ok {
			continue
		}
		entry := *card
		entry.Stats = domfeed.EntryStats{}
		// 以 inbox 条目的发布时间（分值秒精度）为准，保证排序/游标/输出一致。
		entry.PublishedAt = inboxEntry.PublishedAt
		if value, ok := stats[inboxEntry.ClipID]; ok {
			entry.Stats = *value
		}
		entries = append(entries, &entry)
	}
	return entries, nil
}
