// cache_fault_test.go 验证应用层对缓存故障的降级：故障与未命中区分、回源 DB、
// 故障期间不回填、版本读取失败跳过页缓存，并记录 error 指标。
package appfeed_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// recordingMetrics 记录缓存访问结果标签，供故障用例断言指标。
type recordingMetrics struct {
	cacheResults  []string
	limiterScopes []string
}

func (m *recordingMetrics) ObserveHTTP(string, string, time.Duration) {}
func (m *recordingMetrics) ObserveFeed(string, string, time.Duration) {}
func (m *recordingMetrics) ObserveCache(result string) {
	m.cacheResults = append(m.cacheResults, result)
}
func (m *recordingMetrics) ObserveFeedLimiterError(scope string) {
	m.limiterScopes = append(m.limiterScopes, scope)
}
func (m *recordingMetrics) ObserveWorker(string, string)      {}
func (m *recordingMetrics) ObserveQoS(string, int, int, bool) {}

// has 报告是否记录过指定结果标签。
func (m *recordingMetrics) has(result string) bool {
	for _, value := range m.cacheResults {
		if value == result {
			return true
		}
	}
	return false
}

// TestFeedVersionFaultSkipsPageCache 验证版本读取失败时直接回源 DB，不构造摘要也不回填。
func TestFeedVersionFaultSkipsPageCache(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(3)
	cache := newFeedFakeCache(true)
	cache.versionErr = errors.New("redis down")
	metrics := &recordingMetrics{}
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache), appfeed.WithMetrics(metrics))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Limit: 2})
	if err != nil {
		t.Fatalf("版本读取失败不应让读路径报错: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("应回源 DB 返回 2 条，实际 %d 条", len(result.Items))
	}
	if repo.listCalls != 1 {
		t.Fatalf("版本故障时应回源 DB 一次，实际 %d 次", repo.listCalls)
	}
	if cache.getCalls != 0 || cache.setCalls != 0 {
		t.Fatalf("版本故障时不应读写页缓存: get=%d set=%d", cache.getCalls, cache.setCalls)
	}
	if !metrics.has("error") {
		t.Fatalf("版本故障应记录 error 指标，实际 %v", metrics.cacheResults)
	}
}

// TestFeedPageFaultFallsBackWithoutWriteBack 验证页缓存读取故障时回源 DB 且不回填。
func TestFeedPageFaultFallsBackWithoutWriteBack(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(3)
	cache := newFeedFakeCache(true)
	cache.getErr = errors.New("redis down")
	metrics := &recordingMetrics{}
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache), appfeed.WithMetrics(metrics))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Limit: 2})
	if err != nil {
		t.Fatalf("页缓存故障不应让读路径报错: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("应回源 DB 返回 2 条，实际 %d 条", len(result.Items))
	}
	if cache.setCalls != 0 {
		t.Fatalf("故障期间不应把回源结果写进缓存，实际回填 %d 次", cache.setCalls)
	}
	if metrics.has("miss") {
		t.Fatalf("故障不应被记成 miss，实际 %v", metrics.cacheResults)
	}
	if !metrics.has("error") {
		t.Fatalf("页缓存故障应记录 error 指标，实际 %v", metrics.cacheResults)
	}
}

// TestFeedPageMissStillWritesBack 验证真正的未命中仍会回源并回填，保持原有缓存语义。
func TestFeedPageMissStillWritesBack(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(3)
	cache := newFeedFakeCache(true)
	metrics := &recordingMetrics{}
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache), appfeed.WithMetrics(metrics))

	if _, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Limit: 2}); err != nil {
		t.Fatalf("未命中读取失败: %v", err)
	}
	if cache.setCalls != 1 {
		t.Fatalf("未命中共回填一次，实际 %d 次", cache.setCalls)
	}
	if !metrics.has("miss") {
		t.Fatalf("未命中应记录 miss 指标，实际 %v", metrics.cacheResults)
	}
}

// followingFaultRepo 在内存仓储基础上提供 following 场景所需的关注与卡片数据。
type followingFaultRepo struct {
	*feedFakeRepo
	entry *domfeed.FeedEntry
	// cardIDs / statsIDs 记录最近一次批量回源的请求 ID，供负占位用例断言僵尸条目不再被查询。
	cardIDs  []int64
	statsIDs []int64
}

func (r *followingFaultRepo) ListFollowedCreators(_ context.Context, _ int64) ([]*domfeed.FollowedCreator, error) {
	return []*domfeed.FollowedCreator{{CreatorID: r.entry.CreatorID}}, nil
}

func (r *followingFaultRepo) BatchGetEntries(_ context.Context, clipIDs []int64) (map[int64]*domfeed.FeedEntry, error) {
	r.cardIDs = append([]int64(nil), clipIDs...)
	result := make(map[int64]*domfeed.FeedEntry, len(clipIDs))
	for _, id := range clipIDs {
		if id == r.entry.ClipID {
			result[id] = r.entry
		}
	}
	return result, nil
}

func (r *followingFaultRepo) BatchGetStats(_ context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, error) {
	r.statsIDs = append([]int64(nil), clipIDs...)
	result := make(map[int64]*domfeed.EntryStats, len(clipIDs))
	for _, id := range clipIDs {
		result[id] = &domfeed.EntryStats{}
	}
	return result, nil
}

// followingFaultCache 是可注入卡片/计数读取故障的 following 缓存。
type followingFaultCache struct {
	inbox         []domfeed.InboxEntry
	outboxes      map[int64][]domfeed.InboxEntry
	outboxCalls   int
	outboxIDs     []int64
	cardsErr      error
	statsErr      error
	setCards      int
	setStats      int
	negativeCards map[int64]bool
	negativeStats map[int64]bool
}

func (c *followingFaultCache) Enabled() bool { return true }

func (c *followingFaultCache) AddInboxEntries(context.Context, int64, []domfeed.InboxEntry) error {
	return nil
}

func (c *followingFaultCache) ListInbox(_ context.Context, _ int64, _ *domfeed.Cursor, _ int) ([]domfeed.InboxEntry, error) {
	return c.inbox, nil
}

func (c *followingFaultCache) AddOutboxEntries(context.Context, int64, []domfeed.InboxEntry) error {
	return nil
}

func (c *followingFaultCache) ListOutboxes(_ context.Context, creatorIDs []int64, _ *domfeed.Cursor, _ int) ([]domfeed.InboxEntry, error) {
	c.outboxCalls++
	c.outboxIDs = append(c.outboxIDs, creatorIDs...)
	var entries []domfeed.InboxEntry
	for _, creatorID := range creatorIDs {
		entries = append(entries, c.outboxes[creatorID]...)
	}
	return entries, nil
}

func (c *followingFaultCache) GetCards(_ context.Context, clipIDs []int64) (map[int64]*domfeed.FeedEntry, map[int64]bool, error) {
	return nil, filterNegative(clipIDs, c.negativeCards), c.cardsErr
}

func (c *followingFaultCache) SetCards(context.Context, []*domfeed.FeedEntry) { c.setCards++ }

// MarkCardsMissing 记录负占位，模拟 Redis 侧写入。
func (c *followingFaultCache) MarkCardsMissing(_ context.Context, clipIDs []int64) {
	c.negativeCards = rememberNegative(c.negativeCards, clipIDs)
}

func (c *followingFaultCache) GetStats(_ context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, map[int64]bool, error) {
	return nil, filterNegative(clipIDs, c.negativeStats), c.statsErr
}

func (c *followingFaultCache) SetStats(context.Context, map[int64]*domfeed.EntryStats) { c.setStats++ }

// MarkStatsMissing 记录计数负占位。
func (c *followingFaultCache) MarkStatsMissing(_ context.Context, clipIDs []int64) {
	c.negativeStats = rememberNegative(c.negativeStats, clipIDs)
}

// filterNegative 从请求 ID 中筛出命中的负占位集合。
func filterNegative(clipIDs []int64, negative map[int64]bool) map[int64]bool {
	result := make(map[int64]bool, len(clipIDs))
	for _, id := range clipIDs {
		if negative[id] {
			result[id] = true
		}
	}
	return result
}

// rememberNegative 把 clipIDs 并入负占位集合，返回新集合。
func rememberNegative(negative map[int64]bool, clipIDs []int64) map[int64]bool {
	if negative == nil {
		negative = make(map[int64]bool, len(clipIDs))
	}
	for _, id := range clipIDs {
		negative[id] = true
	}
	return negative
}

// newFollowingFaultFixture 构造一条 inbox 条目与可故障注入的缓存。
func newFollowingFaultFixture() (*followingFaultRepo, *followingFaultCache) {
	publishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	entry := &domfeed.FeedEntry{ClipID: 42, CreatorID: 7, PublishedAt: publishedAt}
	repo := &followingFaultRepo{feedFakeRepo: newFeedFakeRepo(0), entry: entry}
	cache := &followingFaultCache{
		inbox: []domfeed.InboxEntry{{ClipID: 42, CreatorID: 7, PublishedAt: publishedAt}},
	}
	return repo, cache
}

// TestFollowingCardCacheFaultLoadsFromDBWithoutWriteBack 验证卡片缓存故障时整批回源且不回填。
func TestFollowingCardCacheFaultLoadsFromDBWithoutWriteBack(t *testing.T) {
	ctx := context.Background()
	repo, cache := newFollowingFaultFixture()
	cache.cardsErr = errors.New("redis down")
	metrics := &recordingMetrics{}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache), appfeed.WithMetrics(metrics))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20})
	if err != nil {
		t.Fatalf("卡片缓存故障不应让关注流报错: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ClipID != 42 {
		t.Fatalf("应回源 DB 返回 clip 42，实际 %+v", result.Items)
	}
	if cache.setCards != 0 {
		t.Fatalf("卡片缓存故障期间不应回填，实际 %d 次", cache.setCards)
	}
	if cache.setStats != 1 {
		t.Fatalf("计数缓存正常时应回填一次，实际 %d 次", cache.setStats)
	}
	if !metrics.has("card_error") {
		t.Fatalf("卡片缓存故障应记录 card_error 指标，实际 %v", metrics.cacheResults)
	}
}

// TestFollowingStatsCacheFaultLoadsFromDBWithoutWriteBack 验证计数缓存故障时整批回源且不回填。
func TestFollowingStatsCacheFaultLoadsFromDBWithoutWriteBack(t *testing.T) {
	ctx := context.Background()
	repo, cache := newFollowingFaultFixture()
	cache.statsErr = errors.New("redis down")
	metrics := &recordingMetrics{}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache), appfeed.WithMetrics(metrics))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20})
	if err != nil {
		t.Fatalf("计数缓存故障不应让关注流报错: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("应回源 DB 返回 1 条，实际 %d 条", len(result.Items))
	}
	if cache.setStats != 0 {
		t.Fatalf("计数缓存故障期间不应回填，实际 %d 次", cache.setStats)
	}
	if cache.setCards != 1 {
		t.Fatalf("卡片缓存正常时应回填一次，实际 %d 次", cache.setCards)
	}
	if !metrics.has("stats_error") {
		t.Fatalf("计数缓存故障应记录 stats_error 指标，实际 %v", metrics.cacheResults)
	}
}
