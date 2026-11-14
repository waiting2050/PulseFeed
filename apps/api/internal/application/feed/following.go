// following.go 实现 following 场景策略：合并读者 inbox 与大 V 创作者 outbox，
// 按实时关注集过滤（取关即消失，无需清除历史 inbox），Redis 不可用时回源 DB 联查分页。
package appfeed

import (
	"context"
	"log"
	"sort"

	appobservability "pulsefeed/internal/application/observability"
	domfeed "pulsefeed/internal/domain/feed"
)

// BigVFollowerThreshold 是大 V 粉丝数阈值：达到该值的创作者走拉模型（读时合并其 outbox），
// 低于该值的创作者走推模型（关注回填/发布扇出时写入读者 inbox）。
const BigVFollowerThreshold = 10000

// inboxRefillRounds 是单次读取中为过滤已取关残留而补取的最大轮数：
// 每轮至少读取一页（默认页约 21 条），3 轮约 63 条；耗尽后不再空转，
// 剩余缺口交给 fallbackFollowingPage 的 DB 回源补齐。
const inboxRefillRounds = 3

// FollowingCache 描述 following 场景所需的缓存能力，由 infra/cache 的 FeedCache 实现。
type FollowingCache interface {
	// Enabled 报告缓存是否启用。
	Enabled() bool
	// AddInboxEntries 把条目推入读者 inbox（关注回填/发布扇出）。
	AddInboxEntries(ctx context.Context, userID int64, entries []domfeed.InboxEntry) error
	// ListInbox 读取读者 inbox 中游标之后的至多 limit 条条目；cursor 为 nil 表示首屏。
	ListInbox(ctx context.Context, userID int64, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error)
	// AddOutboxEntries 把条目推入创作者 outbox（大 V 拉模型）。
	AddOutboxEntries(ctx context.Context, creatorID int64, entries []domfeed.InboxEntry) error
	// ListOutboxes 批量读取多个创作者 outbox 中游标之后的至多 limit 条条目；
	// 实现应把 N 个源的读取合并为一次（必要时分片）pipeline，避免 O(N) 次往返。
	ListOutboxes(ctx context.Context, creatorIDs []int64, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error)
	// GetCards 批量读取卡片缓存，返回命中部分与负占位集（已确认 DB 不存在的 clip）；
	// err 非 nil 表示缓存故障，调用方须整批回源。
	GetCards(ctx context.Context, clipIDs []int64) (map[int64]*domfeed.FeedEntry, map[int64]bool, error)
	// SetCards 批量回填卡片缓存。
	SetCards(ctx context.Context, entries []*domfeed.FeedEntry)
	// MarkCardsMissing 为回源确认不存在的 clip 写短 TTL 负占位，避免僵尸条目反复回源。
	MarkCardsMissing(ctx context.Context, clipIDs []int64)
	// GetStats 批量读取计数缓存，返回命中部分与负占位集；err 非 nil 表示缓存故障，调用方须整批回源。
	GetStats(ctx context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, map[int64]bool, error)
	// SetStats 批量回填计数缓存。
	SetStats(ctx context.Context, stats map[int64]*domfeed.EntryStats)
	// MarkStatsMissing 为回源确认不存在的计数写短 TTL 负占位。
	MarkStatsMissing(ctx context.Context, clipIDs []int64)
}

// FollowingStrategy 是 following 场景策略：
// 读路径合并读者 inbox 与关注中大 V 的 outbox，按实时关注集过滤、排序去重后游标截取，
// 再经卡片/计数两级缓存水合；缓存任一步失败回源 DB（ListFollowingPage）。
type FollowingStrategy struct {
	repo  domfeed.Repository
	cache FollowingCache
	// metrics 记录卡片/计数缓存故障；未注入时静默跳过。
	metrics appobservability.Metrics
}

// NewFollowingStrategy 创建 following 场景策略；cache 为 nil 时恒走 DB 回源。
func NewFollowingStrategy(repo domfeed.Repository, cache FollowingCache) *FollowingStrategy {
	return &FollowingStrategy{repo: repo, cache: cache}
}

// setMetrics 注入可观测性端口，用于记录卡片/计数缓存故障。
func (s *FollowingStrategy) setMetrics(metrics appobservability.Metrics) {
	s.metrics = metrics
}

// observeCache 记录一次缓存访问结果；未注入指标时静默跳过。
func (s *FollowingStrategy) observeCache(result string) {
	if s.metrics != nil {
		s.metrics.ObserveCache(result)
	}
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

	merged, err := s.loadMergedInbox(ctx, req.ViewerID, cursor, count)
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
	entries, err = s.fallbackFollowingPage(ctx, req.ViewerID, cursor, page, entries, count)
	if err != nil {
		return nil, err
	}
	return &Page{Entries: entries}, nil
}

// fallbackFollowingPage 在 inbox/outbox 候选不足一页时按游标回源数据库补齐：
// 覆盖“超过 1000 条被裁剪”导致的漏读，也覆盖缓存为空但 DB 仍有更早内容的情况。
// 回源查询按当前关注关系联查，取关立即生效；HasMore 由补齐后的真实条数决定，
// 因此缓存空不会直接返回空页。
func (s *FollowingStrategy) fallbackFollowingPage(ctx context.Context, viewerID int64, cursor *domfeed.Cursor, page []domfeed.InboxEntry, entries []*domfeed.FeedEntry, count int) ([]*domfeed.FeedEntry, error) {
	if len(entries) >= count {
		return entries, nil
	}
	fallbackCursor := cursor
	if len(page) > 0 {
		last := page[len(page)-1]
		fallbackCursor = &domfeed.Cursor{PublishedAt: last.PublishedAt, ClipID: last.ClipID}
	}

	loaded, err := s.repo.ListFollowingPage(ctx, viewerID, fallbackCursor, count-len(entries))
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]bool, len(entries))
	for _, entry := range entries {
		seen[entry.ClipID] = true
	}
	for _, entry := range loaded {
		if seen[entry.ClipID] {
			continue
		}
		seen[entry.ClipID] = true
		entries = append(entries, entry)
	}
	return entries, nil
}

// loadMergedInbox 读取读者 inbox 与关注中大 V 的 outbox 并合并：
// 每次只按游标取一页（首屏 limit+1 条），outbox 通过一次批量 pipeline 拉取；
// inbox 条目按实时关注集过滤（取关即消失），合并后按 (published_at, clip_id) 严格递减排序去重；
// 关注集或任一缓存读取失败时返回错误，由调用方回源 DB。
func (s *FollowingStrategy) loadMergedInbox(ctx context.Context, viewerID int64, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error) {
	creators, err := s.repo.ListFollowedCreators(ctx, viewerID)
	if err != nil {
		return nil, err
	}
	if len(creators) == 0 {
		return nil, nil
	}

	followed := make(map[int64]bool, len(creators))
	bigVs := make([]int64, 0, len(creators))
	for _, creator := range creators {
		followed[creator.CreatorID] = true
		if creator.FollowerCount >= BigVFollowerThreshold {
			bigVs = append(bigVs, creator.CreatorID)
		}
	}

	inbox, err := s.listInboxPage(ctx, viewerID, cursor, limit, followed)
	if err != nil {
		return nil, err
	}
	outboxes, err := s.listOutboxPages(ctx, bigVs, cursor, limit)
	if err != nil {
		return nil, err
	}

	merged := make([]domfeed.InboxEntry, 0, len(inbox)+len(outboxes))
	merged = append(merged, inbox...)
	merged = append(merged, outboxes...)

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

// listInboxPage 按页读取 inbox 并过滤已取关创作者的残留条目。
// 若过滤后不足一页，以本批最后一条为游标继续向更旧方向补取；
// 补取最多 inboxRefillRounds 轮（约 63 条），耗尽后停止空转，缺口交由 DB 回源补齐。
func (s *FollowingStrategy) listInboxPage(ctx context.Context, viewerID int64, cursor *domfeed.Cursor, limit int, followed map[int64]bool) ([]domfeed.InboxEntry, error) {
	collected := make([]domfeed.InboxEntry, 0, limit)
	next := cursor
	for round := 0; round < inboxRefillRounds && len(collected) < limit; round++ {
		batch, err := s.cache.ListInbox(ctx, viewerID, next, limit-len(collected))
		if err != nil {
			return nil, err
		}
		for _, entry := range batch {
			if followed[entry.CreatorID] {
				collected = append(collected, entry)
			}
		}
		if len(batch) == 0 {
			break
		}
		last := batch[len(batch)-1]
		nextCursor := &domfeed.Cursor{PublishedAt: last.PublishedAt, ClipID: last.ClipID}
		if next != nil && nextCursor.PublishedAt.Equal(next.PublishedAt) && nextCursor.ClipID == next.ClipID {
			break // 游标未推进，说明该批次已到键尾，避免重复读取
		}
		next = nextCursor
	}
	return collected, nil
}

// listOutboxPages 读取关注中大 V 的 outbox：所有源的读取命令合并为一次（必要时分片）pipeline，
// 往返数从 O(N) 降到 O(N/100)，因此不再需要 outboxReadConcurrency 的并发上限；
// 结果顺序与创作者顺序无关（调用方统一排序），任一读取失败即返回错误由上层回源 DB。
func (s *FollowingStrategy) listOutboxPages(ctx context.Context, creatorIDs []int64, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error) {
	if len(creatorIDs) == 0 {
		return nil, nil
	}
	return s.cache.ListOutboxes(ctx, creatorIDs, cursor, limit)
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

	cards, negativeCards, err := s.cache.GetCards(ctx, ids)
	cardsCached := err == nil
	if err != nil {
		// 卡片缓存故障按整批未命中处理：全部回源 DB，且本次不回填。
		log.Printf("warning: 读取卡片缓存失败，整批回源 DB: %v", err)
		s.observeCache("card_error")
		cards = make(map[int64]*domfeed.FeedEntry, len(ids))
		negativeCards = nil
	}
	if cards == nil {
		cards = make(map[int64]*domfeed.FeedEntry, len(ids))
	}
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := cards[id]; ok {
			continue
		}
		if negativeCards[id] {
			// 负占位：此前回源已确认该 clip 不存在，本次直接跳过，不再重复回源。
			continue
		}
		missing = append(missing, id)
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
		if cardsCached {
			s.cache.SetCards(ctx, fresh)
			if absent := missingCardIDs(missing, loaded); len(absent) > 0 {
				// inbox/outbox 里已删作品留下的成员：写短 TTL 负占位，消除每次读取的重复回源。
				s.cache.MarkCardsMissing(ctx, absent)
			}
		}
	}

	// 计数只为真正拿到卡片的 clip 回源：僵尸条目已在卡片阶段被负占位拦下，不再触发计数查询。
	visibleIDs := make([]int64, 0, len(page))
	for _, entry := range page {
		if _, ok := cards[entry.ClipID]; ok {
			visibleIDs = append(visibleIDs, entry.ClipID)
		}
	}

	stats, negativeStats, err := s.cache.GetStats(ctx, visibleIDs)
	statsCached := err == nil
	if err != nil {
		log.Printf("warning: 读取计数缓存失败，整批回源 DB: %v", err)
		s.observeCache("stats_error")
		stats = make(map[int64]*domfeed.EntryStats, len(visibleIDs))
		negativeStats = nil
	}
	if stats == nil {
		stats = make(map[int64]*domfeed.EntryStats, len(visibleIDs))
	}
	missing = missing[:0]
	for _, id := range visibleIDs {
		if _, ok := stats[id]; ok {
			continue
		}
		if negativeStats[id] {
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) > 0 {
		loaded, err := s.repo.BatchGetStats(ctx, missing)
		if err != nil {
			return nil, err
		}
		for id, value := range loaded {
			stats[id] = value
		}
		if statsCached {
			s.cache.SetStats(ctx, loaded)
			if absent := missingStatsIDs(missing, loaded); len(absent) > 0 {
				s.cache.MarkStatsMissing(ctx, absent)
			}
		}
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

// missingCardIDs 返回请求了但 DB 未返回的 clip ID，供写卡片负占位使用。
func missingCardIDs(ids []int64, loaded map[int64]*domfeed.FeedEntry) []int64 {
	absent := make([]int64, 0, len(ids)-len(loaded))
	for _, id := range ids {
		if _, ok := loaded[id]; !ok {
			absent = append(absent, id)
		}
	}
	return absent
}

// missingStatsIDs 返回请求了但 DB 未返回的计数 ID，供写计数负占位使用。
func missingStatsIDs(ids []int64, loaded map[int64]*domfeed.EntryStats) []int64 {
	absent := make([]int64, 0, len(ids)-len(loaded))
	for _, id := range ids {
		if _, ok := loaded[id]; !ok {
			absent = append(absent, id)
		}
	}
	return absent
}
