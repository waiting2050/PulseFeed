// feed_test.go 以内存 fake 覆盖 latest 分页游标、请求校验、读者状态组装与页缓存命中/降级。
package appfeed_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// feedFakeRepo 是内存版 feed 仓储：按 (published_at, clip_id) 递减分页并支持读者状态查询。
type feedFakeRepo struct {
	entries      []*domfeed.FeedEntry
	viewerStates map[int64]map[int64]*domfeed.ViewerState
	listCalls    int
	lastLimit    int
	viewerCalls  int
	listErr      error
	viewerErr    error
}

// newFeedFakeRepo 构造 count 条发布时间递增（越晚越新）的条目。
func newFeedFakeRepo(count int) *feedFakeRepo {
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	entries := make([]*domfeed.FeedEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, &domfeed.FeedEntry{
			ClipID:      int64(i),
			CreatorID:   1,
			Title:       fmt.Sprintf("clip-%d", i),
			PublishedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].PublishedAt.Equal(entries[j].PublishedAt) {
			return entries[i].ClipID > entries[j].ClipID
		}
		return entries[i].PublishedAt.After(entries[j].PublishedAt)
	})
	return &feedFakeRepo{
		entries:      entries,
		viewerStates: make(map[int64]map[int64]*domfeed.ViewerState),
	}
}

func (f *feedFakeRepo) ListLatestPage(_ context.Context, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	f.listCalls++
	f.lastLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]*domfeed.FeedEntry, 0, limit)
	for _, entry := range f.entries {
		if cursor != nil {
			after := entry.PublishedAt.Before(cursor.PublishedAt) ||
				(entry.PublishedAt.Equal(cursor.PublishedAt) && entry.ClipID < cursor.ClipID)
			if !after {
				continue
			}
		}
		result = append(result, entry)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (f *feedFakeRepo) ListFollowingPage(_ context.Context, _ int64, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *feedFakeRepo) ListHotPage(_ context.Context, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *feedFakeRepo) ListFollowedCreators(_ context.Context, _ int64) ([]*domfeed.FollowedCreator, error) {
	return nil, nil
}

func (f *feedFakeRepo) CountFollowers(_ context.Context, _ int64) (int64, error) {
	return 0, nil
}

func (f *feedFakeRepo) ListFollowerIDs(_ context.Context, _ int64) ([]int64, error) {
	return nil, nil
}

func (f *feedFakeRepo) ListCreatorRecentClips(_ context.Context, _ int64, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *feedFakeRepo) ListLatestClipIDs(_ context.Context, _ int) ([]int64, error) {
	return nil, nil
}

func (f *feedFakeRepo) BatchGetEntries(_ context.Context, _ []int64) (map[int64]*domfeed.FeedEntry, error) {
	return map[int64]*domfeed.FeedEntry{}, nil
}

func (f *feedFakeRepo) BatchGetStats(_ context.Context, _ []int64) (map[int64]*domfeed.EntryStats, error) {
	return map[int64]*domfeed.EntryStats{}, nil
}

func (f *feedFakeRepo) BatchGetViewerStates(_ context.Context, viewerID int64, clipIDs []int64) (map[int64]*domfeed.ViewerState, error) {
	f.viewerCalls++
	if f.viewerErr != nil {
		return nil, f.viewerErr
	}
	result := make(map[int64]*domfeed.ViewerState, len(clipIDs))
	for _, id := range clipIDs {
		if state, ok := f.viewerStates[viewerID][id]; ok {
			clone := *state
			result[id] = &clone
		}
	}
	return result, nil
}

// feedFakeCache 是内存页缓存：按摘要存取条目副本，可注入预置命中页模拟共享缓存数据。
type feedFakeCache struct {
	enabled       bool
	pages         map[string][]*domfeed.FeedEntry
	preset        []*domfeed.FeedEntry
	getCalls      int
	hitCalls      int
	setCalls      int
	lastFirstPage bool
}

// newFeedFakeCache 构造指定启用状态的页缓存。
func newFeedFakeCache(enabled bool) *feedFakeCache {
	return &feedFakeCache{enabled: enabled, pages: make(map[string][]*domfeed.FeedEntry)}
}

// feedFakeCloneEntries 返回条目副本，模拟缓存序列化后每次读出独立对象。
func feedFakeCloneEntries(entries []*domfeed.FeedEntry) []*domfeed.FeedEntry {
	result := make([]*domfeed.FeedEntry, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		clone := *entry
		result = append(result, &clone)
	}
	return result
}

func (f *feedFakeCache) Enabled() bool { return f.enabled }

func (f *feedFakeCache) GetPage(_ context.Context, digest string) ([]*domfeed.FeedEntry, bool) {
	f.getCalls++
	if f.preset != nil {
		f.hitCalls++
		return feedFakeCloneEntries(f.preset), true
	}
	entries, ok := f.pages[digest]
	if !ok {
		return nil, false
	}
	f.hitCalls++
	return feedFakeCloneEntries(entries), true
}

func (f *feedFakeCache) SetPage(_ context.Context, digest string, entries []*domfeed.FeedEntry, firstPage bool) {
	f.setCalls++
	f.lastFirstPage = firstPage
	f.pages[digest] = feedFakeCloneEntries(entries)
}

// TestFeedLatestPaginationIsStableAcrossPages 验证 5 条数据按 2 条/页翻页不重不漏且游标正确收尾。
func TestFeedLatestPaginationIsStableAcrossPages(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(5)
	svc := appfeed.New(repo)

	wantSizes := []int{2, 2, 1}
	wantMore := []bool{true, true, false}
	wantIDs := [][]int64{{5, 4}, {3, 2}, {1}}
	seen := make(map[int64]bool, 5)
	cursor := ""
	for page := 0; page < 3; page++ {
		result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("第 %d 页读取失败: %v", page+1, err)
		}
		if len(result.Items) != wantSizes[page] {
			t.Fatalf("第 %d 页条目数 = %d, 期望 %d", page+1, len(result.Items), wantSizes[page])
		}
		for i, item := range result.Items {
			if item.ClipID != wantIDs[page][i] {
				t.Fatalf("第 %d 页第 %d 条 clip_id = %d, 期望 %d", page+1, i, item.ClipID, wantIDs[page][i])
			}
			if seen[item.ClipID] {
				t.Fatalf("跨页出现重复 clip_id: %d", item.ClipID)
			}
			seen[item.ClipID] = true
		}
		if result.HasMore != wantMore[page] {
			t.Fatalf("第 %d 页 has_more = %v, 期望 %v", page+1, result.HasMore, wantMore[page])
		}
		if wantMore[page] && result.NextCursor == "" {
			t.Fatalf("第 %d 页应返回 next_cursor", page+1)
		}
		if !wantMore[page] && result.NextCursor != "" {
			t.Fatalf("末页不应返回 next_cursor: %q", result.NextCursor)
		}
		cursor = result.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("三页应覆盖 5 条数据, 实际 %d 条", len(seen))
	}
	if repo.lastLimit != 3 {
		t.Fatalf("仓储 limit = %d, 期望页大小 +1 = 3", repo.lastLimit)
	}
}

// TestFeedUsesDefaultPageSize 验证 limit=0 取默认 20 并多取一条判定 has_more。
func TestFeedUsesDefaultPageSize(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(21)
	svc := appfeed.New(repo)

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest"})
	if err != nil {
		t.Fatalf("默认页大小读取失败: %v", err)
	}
	if len(result.Items) != domfeed.DefaultFeedLimit {
		t.Fatalf("默认页条目数 = %d, 期望 %d", len(result.Items), domfeed.DefaultFeedLimit)
	}
	if !result.HasMore {
		t.Fatalf("21 条数据按默认页大小应标记 has_more")
	}
	if repo.lastLimit != domfeed.DefaultFeedLimit+1 {
		t.Fatalf("仓储 limit = %d, 期望 %d", repo.lastLimit, domfeed.DefaultFeedLimit+1)
	}

	next, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Cursor: result.NextCursor})
	if err != nil {
		t.Fatalf("第二页读取失败: %v", err)
	}
	if len(next.Items) != 1 || next.HasMore {
		t.Fatalf("第二页应为 1 条且无下一页: %+v", next)
	}
}

// TestFeedRejectsInvalidRequest 验证非法场景、页大小与游标均在访问仓储前被拒。
func TestFeedRejectsInvalidRequest(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		request   *appfeed.FeedRequest
		wantError error
	}{
		{name: "非法场景", request: &appfeed.FeedRequest{Scene: "bogus"}, wantError: domfeed.ErrUnsupportedScene},
		{name: "页大小为负", request: &appfeed.FeedRequest{Scene: "latest", Limit: -1}, wantError: domfeed.ErrInvalidLimit},
		{name: "页大小超上限", request: &appfeed.FeedRequest{Scene: "latest", Limit: domfeed.MaxFeedLimit + 1}, wantError: domfeed.ErrInvalidLimit},
		{name: "非法游标", request: &appfeed.FeedRequest{Scene: "latest", Cursor: "!!!not-base64!!!"}, wantError: domfeed.ErrInvalidCursor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFeedFakeRepo(5)
			svc := appfeed.New(repo)
			if _, err := svc.GetFeed(ctx, tt.request); !errors.Is(err, tt.wantError) {
				t.Fatalf("期望错误 %v，实际 %v", tt.wantError, err)
			}
			if repo.listCalls != 0 {
				t.Fatalf("非法请求不应访问仓储, 实际 %d 次", repo.listCalls)
			}
		})
	}
}

// TestFeedAssemblesViewerState 验证按当前读者实时状态组装，并清空共享条目上的历史状态。
func TestFeedAssemblesViewerState(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(3)
	repo.viewerStates[7] = map[int64]*domfeed.ViewerState{
		1: {Liked: true},
		2: {Saved: true},
	}
	// 模拟页缓存共享对象上残留了其他读者的状态，组装时必须被清空。
	repo.entries[2].Viewer = domfeed.ViewerState{Liked: true, Saved: true}
	svc := appfeed.New(repo)

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", ViewerID: 7, Limit: 3})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("条目数 = %d, 期望 3", len(result.Items))
	}

	states := make(map[int64]domfeed.ViewerState, len(result.Items))
	for _, item := range result.Items {
		states[item.ClipID] = item.Viewer
	}
	if got := states[1]; !got.Liked || got.Saved {
		t.Fatalf("clip 1 读者状态不正确: %+v", got)
	}
	if got := states[2]; got.Liked || !got.Saved {
		t.Fatalf("clip 2 读者状态不正确: %+v", got)
	}
	if got := states[3]; got.Liked || got.Saved {
		t.Fatalf("clip 3 无状态记录，应保持零值: %+v", got)
	}
	if repo.viewerCalls != 1 {
		t.Fatalf("应查询一次读者状态, 实际 %d 次", repo.viewerCalls)
	}
	// 仓储原始条目是共享对象，服务只能改写返回副本，不能就地清空或覆盖。
	if !repo.entries[2].Viewer.Liked || !repo.entries[2].Viewer.Saved {
		t.Fatalf("组装就地修改了仓储共享条目: clip=%d viewer=%+v", repo.entries[2].ClipID, repo.entries[2].Viewer)
	}
}

// TestFeedAnonymousSkipsViewerState 验证未登录读者跳过状态查询且条目状态为零值。
func TestFeedAnonymousSkipsViewerState(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(2)
	repo.entries[0].Viewer = domfeed.ViewerState{Liked: true}
	svc := appfeed.New(repo)

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Limit: 2})
	if err != nil {
		t.Fatalf("匿名读取失败: %v", err)
	}
	for _, item := range result.Items {
		if item.Viewer.Liked || item.Viewer.Saved {
			t.Fatalf("匿名读者状态应为零值: %+v", item.Viewer)
		}
	}
	if repo.viewerCalls != 0 {
		t.Fatalf("匿名读者不应查询状态, 实际 %d 次", repo.viewerCalls)
	}
}

// TestFeedCacheMissThenHit 验证首页 miss 回源并回填，相同请求第二次直接命中缓存不再访问仓储。
func TestFeedCacheMissThenHit(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(3)
	cache := newFeedFakeCache(true)
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache))
	request := &appfeed.FeedRequest{Scene: "latest", Limit: 2}

	first, err := svc.GetFeed(ctx, request)
	if err != nil {
		t.Fatalf("首页读取失败: %v", err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("首页结果不正确: %+v", first)
	}
	if repo.listCalls != 1 {
		t.Fatalf("首页应回源仓储一次, 实际 %d 次", repo.listCalls)
	}
	if cache.setCalls != 1 || !cache.lastFirstPage {
		t.Fatalf("首页 miss 后应回填且标记 first_page: set=%d first=%v", cache.setCalls, cache.lastFirstPage)
	}

	second, err := svc.GetFeed(ctx, request)
	if err != nil {
		t.Fatalf("第二次读取失败: %v", err)
	}
	if repo.listCalls != 1 {
		t.Fatalf("命中缓存后不应再回源仓储, 实际 %d 次", repo.listCalls)
	}
	if cache.hitCalls == 0 {
		t.Fatalf("第二次读取应命中页缓存")
	}
	if len(second.Items) != len(first.Items) || second.NextCursor != first.NextCursor || second.HasMore != first.HasMore {
		t.Fatalf("缓存命中结果与首页不一致: first=%+v second=%+v", first, second)
	}
	for i := range first.Items {
		if second.Items[i].ClipID != first.Items[i].ClipID {
			t.Fatalf("第 %d 条缓存命中 clip_id = %d, 期望 %d", i, second.Items[i].ClipID, first.Items[i].ClipID)
		}
	}
}

// TestFeedCacheDisabledFallsBackToRepo 验证缓存未启用时降级为纯仓储读取且不读写缓存。
func TestFeedCacheDisabledFallsBackToRepo(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(3)
	cache := newFeedFakeCache(false)
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache))
	request := &appfeed.FeedRequest{Scene: "latest", Limit: 2}

	for i := 0; i < 2; i++ {
		if _, err := svc.GetFeed(ctx, request); err != nil {
			t.Fatalf("第 %d 次读取失败: %v", i+1, err)
		}
	}
	if repo.listCalls != 2 {
		t.Fatalf("缓存禁用时应每次回源, 实际 %d 次", repo.listCalls)
	}
	if cache.getCalls != 0 || cache.setCalls != 0 {
		t.Fatalf("缓存禁用时不应访问缓存: get=%d set=%d", cache.getCalls, cache.setCalls)
	}
}

// TestFeedCacheHitClearsStaleViewerState 验证缓存命中返回的共享条目会按当前读者重置并重新组装。
func TestFeedCacheHitClearsStaleViewerState(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cache := newFeedFakeCache(true)
	cache.preset = []*domfeed.FeedEntry{
		{ClipID: 2, CreatorID: 1, Title: "cached-2", PublishedAt: base.Add(2 * time.Hour), Viewer: domfeed.ViewerState{Liked: true, Saved: true}},
		{ClipID: 1, CreatorID: 1, Title: "cached-1", PublishedAt: base.Add(time.Hour), Viewer: domfeed.ViewerState{Liked: true, Saved: true}},
	}
	repo := newFeedFakeRepo(0)
	repo.viewerStates[7] = map[int64]*domfeed.ViewerState{1: {Liked: true}}
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", ViewerID: 7, Limit: 2})
	if err != nil {
		t.Fatalf("缓存命中读取失败: %v", err)
	}
	if repo.listCalls != 0 {
		t.Fatalf("命中缓存不应回源仓储, 实际 %d 次", repo.listCalls)
	}
	if cache.hitCalls != 1 {
		t.Fatalf("应命中缓存一次, 实际 %d 次", cache.hitCalls)
	}
	if len(result.Items) != 2 || result.Items[0].ClipID != 2 || result.Items[1].ClipID != 1 {
		t.Fatalf("缓存命中条目顺序不正确: %+v", result.Items)
	}
	if got := result.Items[0].Viewer; got.Liked || got.Saved {
		t.Fatalf("缓存中残留的 clip 2 状态未被清空: %+v", got)
	}
	if got := result.Items[1].Viewer; !got.Liked || got.Saved {
		t.Fatalf("clip 1 应按当前读者重新组装: %+v", got)
	}
	if repo.viewerCalls != 1 {
		t.Fatalf("应按当前读者查询一次状态, 实际 %d 次", repo.viewerCalls)
	}
}

// TestFeedFollowingRequiresViewer 验证 following 场景未登录直接返回 ErrViewerRequired。
func TestFeedFollowingRequiresViewer(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(3)
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(nil))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", Limit: 2})
	if !errors.Is(err, domfeed.ErrViewerRequired) {
		t.Fatalf("期望 ErrViewerRequired，实际 %v", err)
	}
	if result != nil {
		t.Fatalf("失败时不应返回结果: %+v", result)
	}
	if repo.listCalls != 0 {
		t.Fatalf("following 未登录不应回源 latest, 实际 %d 次", repo.listCalls)
	}
}
