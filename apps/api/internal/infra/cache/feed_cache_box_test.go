// feed_cache_box_test.go 用进程内 miniredis 验证 inbox/outbox 读侧按需取：
// 首屏只取一页、翻页按 score 向更旧方向读取、1000 条裁剪后仍可继续翻页而不全量读。
package cache

import (
	"context"
	"testing"
	"time"

	domfeed "pulsefeed/internal/domain/feed"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newBoxTestCache 启动进程内 Redis 并返回可用的 FeedCache，测试结束自动回收。
func newBoxTestCache(t *testing.T) (*FeedCache, *miniredis.Miniredis) {
	t.Helper()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return &FeedCache{client: client, now: time.Now}, server
}

// seedInbox 写入 count 条发布时间递增（越晚越新）、clipID 依次递增的 inbox 条目。
func seedInbox(t *testing.T, cache *FeedCache, userID int64, count int) time.Time {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	entries := make([]domfeed.InboxEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, domfeed.InboxEntry{
			ClipID:      int64(i),
			CreatorID:   9,
			PublishedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	if err := cache.AddInboxEntries(ctx, userID, entries); err != nil {
		t.Fatalf("写入 inbox 失败: %v", err)
	}
	return base
}

// TestListInboxFirstPageReadsOnlyRequested 验证首屏只取请求条数，而不是把整个键全量读出。
func TestListInboxFirstPageReadsOnlyRequested(t *testing.T) {
	ctx := context.Background()
	cache, _ := newBoxTestCache(t)
	seedInbox(t, cache, 5, 500)

	page, err := cache.ListInbox(ctx, 5, nil, 21)
	if err != nil {
		t.Fatalf("首屏读取失败: %v", err)
	}
	if len(page) != 21 {
		t.Fatalf("首屏应只返回 21 条，实际 %d 条", len(page))
	}
	// 返回结果按发布时间严格递减，且最后一条是第 480 条（500..480）。
	if page[0].ClipID != 500 || page[len(page)-1].ClipID != 480 {
		t.Fatalf("首屏条目不符合最新优先顺序: first=%d last=%d", page[0].ClipID, page[len(page)-1].ClipID)
	}
}

// TestListInboxPaginatesByScore 验证翻页按游标 score 向更旧方向读取，不与上一页重叠。
func TestListInboxPaginatesByScore(t *testing.T) {
	ctx := context.Background()
	cache, _ := newBoxTestCache(t)
	seedInbox(t, cache, 5, 10)

	first, err := cache.ListInbox(ctx, 5, nil, 4)
	if err != nil {
		t.Fatalf("首屏读取失败: %v", err)
	}
	if len(first) != 4 || first[0].ClipID != 10 || first[3].ClipID != 7 {
		t.Fatalf("首屏结果异常: %+v", first)
	}

	cursor := &domfeed.Cursor{PublishedAt: first[3].PublishedAt, ClipID: first[3].ClipID}
	second, err := cache.ListInbox(ctx, 5, cursor, 4)
	if err != nil {
		t.Fatalf("翻页读取失败: %v", err)
	}
	if len(second) != 4 || second[0].ClipID != 6 || second[3].ClipID != 3 {
		t.Fatalf("翻页结果异常: %+v", second)
	}
	for _, entry := range second {
		if entry.ClipID >= cursor.ClipID {
			t.Fatalf("翻页不应包含游标及更新的条目: clip=%d cursor=%d", entry.ClipID, cursor.ClipID)
		}
	}
}

// TestListInboxSameSecondUsesClipBoundary 验证同一毫秒内的条目按 clip_id 复合边界翻页，
// 不会因为 ZSET 分数相同而重复或遗漏。
func TestListInboxSameSecondUsesClipBoundary(t *testing.T) {
	ctx := context.Background()
	cache, _ := newBoxTestCache(t)
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	entries := []domfeed.InboxEntry{
		{ClipID: 1, CreatorID: 9, PublishedAt: at},
		{ClipID: 2, CreatorID: 9, PublishedAt: at},
		{ClipID: 3, CreatorID: 9, PublishedAt: at},
	}
	if err := cache.AddInboxEntries(ctx, 5, entries); err != nil {
		t.Fatalf("写入 inbox 失败: %v", err)
	}

	page, err := cache.ListInbox(ctx, 5, &domfeed.Cursor{PublishedAt: at, ClipID: 2}, 10)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(page) != 1 || page[0].ClipID != 1 {
		t.Fatalf("同秒游标应只返回更小的 clip_id=1，实际 %+v", page)
	}
}

// TestAddInboxStoresMillisecondScore 验证写入侧分值使用毫秒：
// 同一秒内不同毫秒的条目分值不同，游标停在其中一条时只重读该毫秒，而不是整秒。
func TestAddInboxStoresMillisecondScore(t *testing.T) {
	ctx := context.Background()
	cache, server := newBoxTestCache(t)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	entries := []domfeed.InboxEntry{
		{ClipID: 1, CreatorID: 9, PublishedAt: base},
		{ClipID: 2, CreatorID: 9, PublishedAt: base.Add(7 * time.Millisecond)},
	}
	if err := cache.AddInboxEntries(ctx, 5, entries); err != nil {
		t.Fatalf("写入 inbox 失败: %v", err)
	}

	scores := make(map[string]float64, len(entries))
	for _, entry := range entries {
		member := feedBoxMember(entry.ClipID, entry.CreatorID)
		score, err := server.ZScore(inboxKey(5), member)
		if err != nil {
			t.Fatalf("读取成员 %s 分值失败: %v", member, err)
		}
		scores[member] = score
	}
	for _, entry := range entries {
		member := feedBoxMember(entry.ClipID, entry.CreatorID)
		if got := int64(scores[member]); got != entry.PublishedAt.UnixMilli() {
			t.Fatalf("成员 %s 的分值应为毫秒 %d，实际 %d", member, entry.PublishedAt.UnixMilli(), got)
		}
	}

	page, err := cache.ListInbox(ctx, 5, &domfeed.Cursor{PublishedAt: entries[1].PublishedAt, ClipID: 2}, 10)
	if err != nil {
		t.Fatalf("按毫秒游标翻页失败: %v", err)
	}
	if len(page) != 1 || page[0].ClipID != 1 {
		t.Fatalf("同秒更早的条目应可继续翻到，实际 %+v", page)
	}
	if !page[0].PublishedAt.Equal(base) {
		t.Fatalf("毫秒分值往返应还原原始发布时间，实际 %v", page[0].PublishedAt)
	}
}

// TestListInboxIgnoresLegacySecondKey 验证毫秒改造后读路径只认 pf:v2 键：
// pf:v1 旧键里按秒存的分值不会被当作毫秒解读，旧键保持原样由既有 TTL 回收。
func TestListInboxIgnoresLegacySecondKey(t *testing.T) {
	ctx := context.Background()
	cache, server := newBoxTestCache(t)
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	legacyKey := "pf:v1:feed:inbox:5"
	if _, err := server.ZAdd(legacyKey, float64(at.Unix()), feedBoxMember(1, 9)); err != nil {
		t.Fatalf("写入旧键失败: %v", err)
	}
	server.SetTTL(legacyKey, 24*time.Hour)

	page, err := cache.ListInbox(ctx, 5, nil, 10)
	if err != nil {
		t.Fatalf("读取 inbox 失败: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("不应读取 pf:v1 旧键，实际返回 %+v", page)
	}
	if _, err := server.ZMembers(legacyKey); err != nil {
		t.Fatalf("旧键应保持原样由 TTL 回收: %v", err)
	}

	if err := cache.AddInboxEntries(ctx, 5, []domfeed.InboxEntry{{ClipID: 2, CreatorID: 9, PublishedAt: at}}); err != nil {
		t.Fatalf("写入新键失败: %v", err)
	}
	if members, err := server.ZMembers(inboxKey(5)); err != nil || len(members) != 1 {
		t.Fatalf("新写入应落到 %s，实际 members=%v err=%v", inboxKey(5), members, err)
	}
}

// TestListInboxAfterTrimKeepsPagingDepth 验证 1000 条裁剪保留后，
// 读侧仍只按页取数据（不全量读），并能沿游标一直翻到最旧一条。
func TestListInboxAfterTrimKeepsPagingDepth(t *testing.T) {
	ctx := context.Background()
	cache, server := newBoxTestCache(t)
	seedInbox(t, cache, 5, inboxMaxEntries+5)

	members, err := server.ZMembers(inboxKey(5))
	if err != nil {
		t.Fatalf("读取键成员失败: %v", err)
	}
	if size := len(members); size != inboxMaxEntries {
		t.Fatalf("裁剪后键内应为 %d 条，实际 %d 条", inboxMaxEntries, size)
	}

	seen := 0
	var cursor *domfeed.Cursor
	for {
		page, err := cache.ListInbox(ctx, 5, cursor, 100)
		if err != nil {
			t.Fatalf("分页读取失败: %v", err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 100 {
			t.Fatalf("单页不应超过请求条数: %d", len(page))
		}
		seen += len(page)
		last := page[len(page)-1]
		cursor = &domfeed.Cursor{PublishedAt: last.PublishedAt, ClipID: last.ClipID}
	}
	if seen != inboxMaxEntries {
		t.Fatalf("沿游标应翻到全部 %d 条，实际 %d 条", inboxMaxEntries, seen)
	}
	if cursor == nil || cursor.ClipID != 6 {
		t.Fatalf("最旧一条应为 clip_id=6（前 5 条被裁剪），实际 cursor=%+v", cursor)
	}
}

// TestListInboxDisabledReturnsUnavailable 验证缓存未启用时返回 ErrFeedBoxUnavailable，
// 由上层策略回源数据库。
func TestListInboxDisabledReturnsUnavailable(t *testing.T) {
	ctx := context.Background()
	cache := &FeedCache{}
	if _, err := cache.ListInbox(ctx, 5, nil, 10); err != ErrFeedBoxUnavailable {
		t.Fatalf("期望 ErrFeedBoxUnavailable，实际 %v", err)
	}
}

// TestAddFeedBoxRenewsTTL 验证 inbox/outbox 写入时设置并续期 TTL：
// 活跃键不会无限增长，沉寂键到期后由 DB 回源兜底。
func TestAddFeedBoxRenewsTTL(t *testing.T) {
	ctx := context.Background()
	cache, server := newBoxTestCache(t)
	seedInbox(t, cache, 5, 2)
	if err := cache.AddOutboxEntries(ctx, 7, []domfeed.InboxEntry{{
		ClipID: 9, CreatorID: 7, PublishedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("写入 outbox 失败: %v", err)
	}

	for _, key := range []string{inboxKey(5), outboxKey(7)} {
		ttl := server.TTL(key)
		if ttl < feedBoxTTL || ttl > feedBoxTTL*120/100 {
			t.Fatalf("键 %s 的 TTL 应在基础值到 +20%% 抖动之间，实际 %v", key, ttl)
		}
	}
}

// TestListOutboxPaginatesByScore 验证 outbox 与 inbox 共用按需分页语义。
func TestListOutboxPaginatesByScore(t *testing.T) {
	ctx := context.Background()
	cache, _ := newBoxTestCache(t)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	entries := make([]domfeed.InboxEntry, 0, 6)
	for i := 1; i <= 6; i++ {
		entries = append(entries, domfeed.InboxEntry{
			ClipID:      int64(100 + i),
			CreatorID:   7,
			PublishedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	if err := cache.AddOutboxEntries(ctx, 7, entries); err != nil {
		t.Fatalf("写入 outbox 失败: %v", err)
	}

	first, err := cache.ListOutbox(ctx, 7, nil, 2)
	if err != nil {
		t.Fatalf("outbox 首屏读取失败: %v", err)
	}
	if len(first) != 2 || first[0].ClipID != 106 || first[1].ClipID != 105 {
		t.Fatalf("outbox 首屏异常: %+v", first)
	}
	cursor := &domfeed.Cursor{PublishedAt: first[1].PublishedAt, ClipID: first[1].ClipID}
	second, err := cache.ListOutbox(ctx, 7, cursor, 2)
	if err != nil {
		t.Fatalf("outbox 翻页读取失败: %v", err)
	}
	if len(second) != 2 || second[0].ClipID != 104 || second[1].ClipID != 103 {
		t.Fatalf("outbox 翻页异常: %+v", second)
	}
}
