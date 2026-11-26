// feed_cache_negative_test.go 验证卡片/计数负占位的读写与 TTL，以及新内容覆盖负占位后立即可见。
package cache

import (
	"context"
	"testing"

	domfeed "pulsefeed/internal/domain/feed"
)

// TestFeedCacheCardsNegativePlaceholder 验证卡片负占位：读到即标记 negative、DB 不可用不再回源，
// 且发布路径用真实卡片覆盖同键后立即恢复可见。
func TestFeedCacheCardsNegativePlaceholder(t *testing.T) {
	cache, server := newFaultTestCache(t)
	ctx := context.Background()

	cache.MarkCardsMissing(ctx, []int64{7})

	cards, negative, err := cache.GetCards(ctx, []int64{7, 8})
	if err != nil {
		t.Fatalf("读取负占位失败: %v", err)
	}
	if len(cards) != 0 || !negative[7] || negative[8] {
		t.Fatalf("负占位应只标记已确认缺失的 ID: cards=%v negative=%v", cards, negative)
	}
	if ttl := server.TTL(cardKey(7)); ttl <= 0 || ttl > 2*missingCardTTL {
		t.Fatalf("负占位 TTL 应在 (%v, %v] 内，实际 %v", 0, 2*missingCardTTL, ttl)
	}

	// 新发布/更新路径会以真实卡片覆盖同键：负占位不得挡住正常新内容。
	cache.SetCards(ctx, []*domfeed.FeedEntry{{ClipID: 7, CreatorID: 1, Title: "新发布"}})
	cards, negative, err = cache.GetCards(ctx, []int64{7})
	if err != nil {
		t.Fatalf("覆盖后读取失败: %v", err)
	}
	if len(cards) != 1 || cards[7].Title != "新发布" || negative[7] {
		t.Fatalf("真实卡片应覆盖负占位: cards=%v negative=%v", cards, negative)
	}
}

// TestFeedCacheStatsNegativePlaceholder 验证计数负占位独立于卡片，并带同样的短 TTL。
func TestFeedCacheStatsNegativePlaceholder(t *testing.T) {
	cache, server := newFaultTestCache(t)
	ctx := context.Background()

	cache.MarkStatsMissing(ctx, []int64{9})

	stats, negative, err := cache.GetStats(ctx, []int64{9, 10})
	if err != nil {
		t.Fatalf("读取计数负占位失败: %v", err)
	}
	if len(stats) != 0 || !negative[9] || negative[10] {
		t.Fatalf("计数负占位应只标记已确认缺失的 ID: stats=%v negative=%v", stats, negative)
	}
	if ttl := server.TTL(statsKey(9)); ttl <= 0 || ttl > 2*missingCardTTL {
		t.Fatalf("计数负占位 TTL 应在 (%v, %v] 内，实际 %v", 0, 2*missingCardTTL, ttl)
	}

	// 新计数覆盖负占位后立即生效。
	cache.SetStats(ctx, map[int64]*domfeed.EntryStats{9: {LikeCount: 1}})
	stats, negative, err = cache.GetStats(ctx, []int64{9})
	if err != nil {
		t.Fatalf("覆盖后读取失败: %v", err)
	}
	if len(stats) != 1 || stats[9].LikeCount != 1 || negative[9] {
		t.Fatalf("真实计数应覆盖负占位: stats=%v negative=%v", stats, negative)
	}
}
