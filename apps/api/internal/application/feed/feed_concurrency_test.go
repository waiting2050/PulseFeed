// feed_concurrency_test.go 用线程安全 fake 验证 Feed 页缓存的两条并发性质：
// 同一 miss 页被 100 个并发请求命中时底层只回源一次且结果一致；
// 共享缓存条目按读者逐条拷贝清空，A 用户的 liked/saved 不会泄露给 B 用户。
package appfeed_test

import (
	"context"
	"sync"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// concurrentFeedRepo 给内存仓储的取数与读者状态查询加锁，供并发测试安全统计回源次数。
type concurrentFeedRepo struct {
	*feedFakeRepo
	mu sync.Mutex
}

// ListLatestPage 在锁内委托内存仓储，保证并发回源计数准确。
func (r *concurrentFeedRepo) ListLatestPage(ctx context.Context, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.feedFakeRepo.ListLatestPage(ctx, cursor, limit)
}

// BatchGetViewerStates 在锁内委托内存仓储，避免并发读写 viewerStates。
func (r *concurrentFeedRepo) BatchGetCreatorProfiles(ctx context.Context, creatorIDs []int64) (map[int64]*domfeed.CreatorProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.feedFakeRepo.BatchGetCreatorProfiles(ctx, creatorIDs)
}

func (r *concurrentFeedRepo) BatchGetFollowedCreatorIDs(ctx context.Context, viewerID int64, creatorIDs []int64) (map[int64]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.feedFakeRepo.BatchGetFollowedCreatorIDs(ctx, viewerID, creatorIDs)
}

func (r *concurrentFeedRepo) BatchGetViewerStates(ctx context.Context, viewerID int64, clipIDs []int64) (map[int64]*domfeed.ViewerState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.feedFakeRepo.BatchGetViewerStates(ctx, viewerID, clipIDs)
}

// listCallCount 返回累计回源次数。
func (r *concurrentFeedRepo) listCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

// viewerCallCount 返回累计读者状态查询次数。
func (r *concurrentFeedRepo) viewerCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewerCalls
}

// concurrentPageCache 是并发安全的页缓存。
// shared=true 时每次返回“新切片 + 同一批条目对象”，模拟最坏情况的跨请求共享缓存对象；
// shared=false 时按摘要存取独立副本，用于验证 singleflight 合并后的回填与命中。
type concurrentPageCache struct {
	mu       sync.Mutex
	shared   bool
	preset   []*domfeed.FeedEntry
	pages    map[string][]*domfeed.FeedEntry
	getCalls int
	hitCalls int
	setCalls int
}

// newConcurrentPageCache 构造并发页缓存；preset 仅在 shared 模式下生效。
func newConcurrentPageCache(shared bool, preset ...*domfeed.FeedEntry) *concurrentPageCache {
	return &concurrentPageCache{
		shared: shared,
		preset: preset,
		pages:  make(map[string][]*domfeed.FeedEntry),
	}
}

// Enabled 报告缓存已启用。
func (c *concurrentPageCache) Enabled() bool { return true }

// GetPage 按模式返回缓存页：shared 模式共享条目对象，普通模式返回独立副本。
func (c *concurrentPageCache) GetPage(_ context.Context, digest string) ([]*domfeed.FeedEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	if c.shared {
		c.hitCalls++
		// 新切片、共享条目对象：只有按条目拷贝才能隔离不同读者的状态。
		return append([]*domfeed.FeedEntry(nil), c.preset...), true
	}
	entries, ok := c.pages[digest]
	if !ok {
		return nil, false
	}
	c.hitCalls++
	return feedFakeCloneEntries(entries), true
}

// SetPage 回填页缓存并统计次数。
func (c *concurrentPageCache) SetPage(_ context.Context, digest string, entries []*domfeed.FeedEntry, _ bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls++
	c.pages[digest] = feedFakeCloneEntries(entries)
}

// feedConcurrentOutcome 汇总一次并发请求的结果。
type feedConcurrentOutcome struct {
	viewerID int64
	result   *appfeed.FeedResult
	err      error
}

// TestFeedPageCacheSingleflightMergesConcurrentMisses 验证 100 个 goroutine 打同一 miss 页时
// 底层仓储只回源一次、缓存只回填一次，且所有请求返回一致的分页结果。
func TestFeedPageCacheSingleflightMergesConcurrentMisses(t *testing.T) {
	ctx := context.Background()
	repo := &concurrentFeedRepo{feedFakeRepo: newFeedFakeRepo(3)}
	cache := newConcurrentPageCache(false)
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache))
	request := &appfeed.FeedRequest{Scene: "latest", Limit: 2}

	const goroutines = 100
	results := make(chan feedConcurrentOutcome, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			result, err := svc.GetFeed(ctx, request)
			results <- feedConcurrentOutcome{result: result, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var first *appfeed.FeedResult
	successes := 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("并发读取失败: %v", outcome.err)
		}
		successes++
		if first == nil {
			first = outcome.result
			continue
		}
		if len(outcome.result.Items) != len(first.Items) ||
			outcome.result.NextCursor != first.NextCursor ||
			outcome.result.HasMore != first.HasMore {
			t.Fatalf("并发请求返回的分页结果不一致: first=%+v got=%+v", first, outcome.result)
		}
		for i := range first.Items {
			if outcome.result.Items[i].ClipID != first.Items[i].ClipID {
				t.Fatalf("第 %d 条 clip_id = %d, 期望与首个结果一致 %d",
					i, outcome.result.Items[i].ClipID, first.Items[i].ClipID)
			}
		}
	}
	if successes != goroutines {
		t.Fatalf("成功请求数 = %d, 期望 %d", successes, goroutines)
	}
	if got := repo.listCallCount(); got != 1 {
		t.Fatalf("并发 miss 时底层回源 = %d 次, 期望 singleflight 合并为 1 次", got)
	}
	if cache.setCalls != 1 {
		t.Fatalf("页缓存回填 = %d 次, 期望 1 次", cache.setCalls)
	}
	if cache.getCalls < goroutines {
		t.Fatalf("页缓存读取 = %d 次, 期望至少 %d 次（每个请求先查缓存）", cache.getCalls, goroutines)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("分页结果不正确: %+v", first)
	}
}

// TestFeedSharedCacheEntriesIsolateViewersAcrossUsers 验证共享缓存条目被不同读者并发消费时，
// 每条按读者拷贝并清空 Viewer，A 的 liked 不会泄露给 B，B 的 saved 也不会污染 A。
func TestFeedSharedCacheEntriesIsolateViewersAcrossUsers(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	shared := []*domfeed.FeedEntry{
		{ClipID: 2, CreatorID: 1, Title: "cached-2", PublishedAt: base.Add(2 * time.Hour)},
		{ClipID: 1, CreatorID: 1, Title: "cached-1", PublishedAt: base.Add(time.Hour)},
	}

	repo := &concurrentFeedRepo{feedFakeRepo: newFeedFakeRepo(0)}
	repo.viewerStates[101] = map[int64]*domfeed.ViewerState{1: {Liked: true}}
	repo.viewerStates[202] = map[int64]*domfeed.ViewerState{1: {Saved: true}}

	cache := newConcurrentPageCache(true, shared...)
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache))

	const perUser = 50
	results := make(chan feedConcurrentOutcome, perUser*2)
	var wg sync.WaitGroup
	for i := 0; i < perUser; i++ {
		for _, viewerID := range []int64{101, 202} {
			wg.Add(1)
			go func(viewerID int64) {
				defer wg.Done()
				result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{
					Scene:    "latest",
					ViewerID: viewerID,
					Limit:    2,
				})
				results <- feedConcurrentOutcome{viewerID: viewerID, result: result, err: err}
			}(viewerID)
		}
	}
	wg.Wait()
	close(results)

	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("读者 %d 并发读取失败: %v", outcome.viewerID, outcome.err)
		}
		items := outcome.result.Items
		if len(items) != 2 || items[0].ClipID != 2 || items[1].ClipID != 1 {
			t.Fatalf("读者 %d 返回条目不正确: %+v", outcome.viewerID, items)
		}
		clip1 := items[1].Viewer
		switch outcome.viewerID {
		case 101:
			if !clip1.Liked || clip1.Saved {
				t.Fatalf("读者 101 的 clip 1 状态 = %+v, 期望 liked=true saved=false", clip1)
			}
		case 202:
			if clip1.Liked || !clip1.Saved {
				t.Fatalf("读者 202 的 clip 1 状态 = %+v, 期望 liked=false saved=true（A 的 liked 不得泄露）", clip1)
			}
		default:
			t.Fatalf("未预期的读者 %d", outcome.viewerID)
		}
		if got := items[0].Viewer; got.Liked || got.Saved {
			t.Fatalf("读者 %d 的 clip 2 状态 = %+v, 该读者对 clip 2 无状态记录，应为零值", outcome.viewerID, got)
		}
	}

	if got := repo.listCallCount(); got != 0 {
		t.Fatalf("共享缓存全部命中时不应回源仓储, 实际回源 %d 次", got)
	}
	if got := repo.viewerCallCount(); got != perUser*2 {
		t.Fatalf("读者状态查询 = %d 次, 期望每请求一次共 %d 次", got, perUser*2)
	}
	// 共享条目对象本身必须保持零值：服务只能改写返回副本，不能把某个读者的状态写回缓存对象。
	for _, entry := range shared {
		if entry.Viewer.Liked || entry.Viewer.Saved {
			t.Fatalf("共享缓存条目 clip_id=%d 的 Viewer 被就地改写: %+v", entry.ClipID, entry.Viewer)
		}
	}
}
