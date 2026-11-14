// following_fallback_test.go 验证 following 流在 inbox/outbox 候选耗尽时回源数据库补齐，
// 且 HasMore 按补齐后的真实条数判定，缓存为空不会直接返回空页。
package appfeed_test

import (
	"context"
	"sort"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// fallbackFollowingRepo 提供固定的关注集与 DB 回源页，并记录回源游标与条数。
type fallbackFollowingRepo struct {
	*feedFakeRepo
	creators   []*domfeed.FollowedCreator
	cards      map[int64]*domfeed.FeedEntry
	page       []*domfeed.FeedEntry
	pageCalls  int
	lastCursor *domfeed.Cursor
	lastLimit  int
}

func (r *fallbackFollowingRepo) BatchGetEntries(_ context.Context, clipIDs []int64) (map[int64]*domfeed.FeedEntry, error) {
	result := make(map[int64]*domfeed.FeedEntry, len(clipIDs))
	for _, id := range clipIDs {
		if card, ok := r.cards[id]; ok {
			result[id] = card
		}
	}
	return result, nil
}

func (r *fallbackFollowingRepo) ListFollowedCreators(_ context.Context, _ int64) ([]*domfeed.FollowedCreator, error) {
	return r.creators, nil
}

func (r *fallbackFollowingRepo) ListFollowingPage(_ context.Context, _ int64, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	r.pageCalls++
	r.lastCursor = cursor
	r.lastLimit = limit
	result := make([]*domfeed.FeedEntry, 0, limit)
	for _, entry := range r.page {
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

// newFallbackFollowingRepo 构造 count 条关注创作者 7 的作品，发布时间严格递减。
func newFallbackFollowingRepo(count int) *fallbackFollowingRepo {
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	page := make([]*domfeed.FeedEntry, 0, count)
	for i := 1; i <= count; i++ {
		page = append(page, &domfeed.FeedEntry{
			ClipID:      int64(i),
			CreatorID:   7,
			Title:       "fallback",
			PublishedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	// 与仓储分页一致：按发布时间递减存放，便于断言首条是最新内容。
	sort.Slice(page, func(i, j int) bool { return page[i].PublishedAt.After(page[j].PublishedAt) })
	return &fallbackFollowingRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		creators:     []*domfeed.FollowedCreator{{CreatorID: 7}},
		cards:        make(map[int64]*domfeed.FeedEntry),
		page:         page,
	}
}

// TestFollowingFallsBackToDBWhenCacheEmpty 验证缓存候选为空时回源 DB 补齐整页，
// 并按补齐后的真实条数返回 HasMore=true 与下一页游标。
func TestFollowingFallsBackToDBWhenCacheEmpty(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(40)
	cache := &followingFaultCache{} // inbox/outbox 均为空，模拟超过裁剪深度后的缓存耗尽
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20})
	if err != nil {
		t.Fatalf("关注流回源失败: %v", err)
	}
	if len(result.Items) != 20 {
		t.Fatalf("应回源补齐 20 条，实际 %d 条", len(result.Items))
	}
	if !result.HasMore || result.NextCursor == "" {
		t.Fatalf("DB 仍有更早内容时应 HasMore=true 且返回游标: %+v", result)
	}
	if repo.pageCalls != 1 || repo.lastLimit != 21 || repo.lastCursor != nil {
		t.Fatalf("首屏回源应无游标取 21 条，实际 calls=%d limit=%d cursor=%+v", repo.pageCalls, repo.lastLimit, repo.lastCursor)
	}
}

// TestFollowingFallbackReportsNoMoreWhenDBExhausted 验证缓存与 DB 都读到底时返回全部条目
// 且 HasMore=false，不因缓存为空而误判为空页。
func TestFollowingFallbackReportsNoMoreWhenDBExhausted(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(3)
	cache := &followingFaultCache{}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20})
	if err != nil {
		t.Fatalf("关注流回源失败: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("应返回 DB 中全部 3 条，实际 %d 条", len(result.Items))
	}
	if result.HasMore {
		t.Fatalf("DB 已读到底时 HasMore 应为 false: %+v", result)
	}
}

// TestFollowingLegacyCursorResetsToFirstPage 验证旧客户端的秒级游标（无版本字段）不会报错，
// 而是显式降级为首屏：回源不带游标，响应从最新一条重新开始，后续游标带新版本号。
func TestFollowingLegacyCursorResetsToFirstPage(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(40)
	cache := &followingFaultCache{}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	// 模拟毫秒改造前编码的游标：时间只到秒，没有版本字段。
	legacy := domfeed.EncodeCursor(&domfeed.Cursor{
		PublishedAt: time.Date(2026, 3, 1, 12, 0, 30, 0, time.UTC),
		ClipID:      30,
	})
	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{
		Scene: "following", ViewerID: 5, Cursor: legacy, Limit: 20,
	})
	if err != nil {
		t.Fatalf("旧游标不应导致关注流报错: %v", err)
	}
	if repo.lastCursor != nil {
		t.Fatalf("旧游标应重置为首屏，实际回源游标 %+v", repo.lastCursor)
	}
	if len(result.Items) != 20 || result.Items[0].ClipID != 40 {
		t.Fatalf("重置后应从最新一条开始，实际首条 clip=%d 共 %d 条", result.Items[0].ClipID, len(result.Items))
	}

	decoded, err := domfeed.DecodeCursor(result.NextCursor)
	if err != nil {
		t.Fatalf("新游标应可解析: %v", err)
	}
	if decoded.Version != domfeed.CursorVersionMilli {
		t.Fatalf("新游标应携带毫秒版本 %d，实际 %d", domfeed.CursorVersionMilli, decoded.Version)
	}
}

// TestFollowingVersionedCursorKeepsPaging 验证带毫秒版本的新游标正常续翻，不被重置。
func TestFollowingVersionedCursorKeepsPaging(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(40)
	cache := &followingFaultCache{}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	cursor := domfeed.EncodeCursor(&domfeed.Cursor{
		PublishedAt: time.Date(2026, 3, 1, 12, 0, 30, 0, time.UTC),
		ClipID:      30,
		Version:     domfeed.CursorVersionMilli,
	})
	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{
		Scene: "following", ViewerID: 5, Cursor: cursor, Limit: 5,
	})
	if err != nil {
		t.Fatalf("关注流续翻失败: %v", err)
	}
	if repo.lastCursor == nil || repo.lastCursor.ClipID != 30 {
		t.Fatalf("新版本游标应原样传给回源查询，实际 %+v", repo.lastCursor)
	}
	if len(result.Items) != 5 || result.Items[0].ClipID != 29 {
		t.Fatalf("续翻应从游标之后开始，实际首条 clip=%d", result.Items[0].ClipID)
	}
}

// TestFollowingReadsAllOutboxesInOneBatch 验证一次关注流读取把所有大 V outbox 合并为一次批量读取，
// 而不是每个创作者一次 Redis 往返。
func TestFollowingReadsAllOutboxesInOneBatch(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(0)
	repo.creators = []*domfeed.FollowedCreator{
		{CreatorID: 7, FollowerCount: appfeed.BigVFollowerThreshold},
		{CreatorID: 8, FollowerCount: appfeed.BigVFollowerThreshold},
	}
	cache := &followingFaultCache{}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	if _, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 5}); err != nil {
		t.Fatalf("关注流读取失败: %v", err)
	}
	if cache.outboxCalls != 1 {
		t.Fatalf("大 V outbox 应合并为一次批量读取，实际调用 %d 次", cache.outboxCalls)
	}
	if len(cache.outboxIDs) != 2 || cache.outboxIDs[0] != 7 || cache.outboxIDs[1] != 8 {
		t.Fatalf("批量读取应一次带上全部大 V，实际 %v", cache.outboxIDs)
	}
}

// TestFollowingFallbackUsesLastCacheEntryCursor 验证缓存与 DB 混合时，
// 回源游标取缓存页最后一条，避免重复返回已读内容。
func TestFollowingFallbackUsesLastCacheEntryCursor(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(40)
	publishedAt := time.Date(2026, 3, 1, 13, 0, 0, 0, time.UTC)
	cache := &followingFaultCache{
		inbox: []domfeed.InboxEntry{{ClipID: 100, CreatorID: 7, PublishedAt: publishedAt}},
	}
	repo.cards[100] = &domfeed.FeedEntry{ClipID: 100, CreatorID: 7, PublishedAt: publishedAt}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 4})
	if err != nil {
		t.Fatalf("关注流回源失败: %v", err)
	}
	if len(result.Items) != 4 {
		t.Fatalf("应返回缓存 1 条 + DB 补齐 3 条，实际 %d 条", len(result.Items))
	}
	if result.Items[0].ClipID != 100 {
		t.Fatalf("缓存条目应排在最前，实际首条 clip=%d", result.Items[0].ClipID)
	}
	if repo.lastCursor == nil || repo.lastCursor.ClipID != 100 || !repo.lastCursor.PublishedAt.Equal(publishedAt) {
		t.Fatalf("回源游标应取缓存页最后一条，实际 %+v", repo.lastCursor)
	}
}
