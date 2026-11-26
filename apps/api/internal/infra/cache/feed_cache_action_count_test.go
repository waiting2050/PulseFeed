// feed_cache_action_count_test.go 用进程内 miniredis 验证互动计数的「库种子 + 分片增量 + TTL」模型：
// 首次访问按 pf_clip_stats 种子化、增量持续累加、取消降回 0 后不再下探、
// base 单独过期或整体 TTL 到期后重种子化不重复计、合计结果始终钳制为非负。
package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	domfeed "pulsefeed/internal/domain/feed"
	dominteraction "pulsefeed/internal/domain/interaction"
)

// fakeActionStats 是可控的 pf_clip_stats 读取端口，模拟 DB 基线随 Worker 落库推进。
type fakeActionStats struct {
	mu    sync.Mutex
	stats map[int64]domfeed.EntryStats
}

// newFakeActionStats 创建空的假基线读取器。
func newFakeActionStats() *fakeActionStats {
	return &fakeActionStats{stats: make(map[int64]domfeed.EntryStats)}
}

// BatchGetStats 实现 ActionStatsReader；缺失的 clip 不出现在结果集中（调用方按 0 处理）。
func (f *fakeActionStats) BatchGetStats(_ context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	found := make(map[int64]*domfeed.EntryStats, len(clipIDs))
	for _, clipID := range clipIDs {
		if stats, ok := f.stats[clipID]; ok {
			copied := stats
			found[clipID] = &copied
		}
	}
	return found, nil
}

// set 覆盖某个 clip 的 DB 基线，模拟 Worker 落库后的 pf_clip_stats。
func (f *fakeActionStats) set(clipID, like, save int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats[clipID] = domfeed.EntryStats{LikeCount: like, SaveCount: save}
}

// readActionBase 读取 base hash 的 like/save 字段。
func readActionBase(t *testing.T, cache *FeedCache, clipID int64) (int64, int64) {
	t.Helper()
	values, err := cache.client.HMGet(context.Background(), actionBaseKey(clipID), actionBaseFieldLike, actionBaseFieldSave).Result()
	if err != nil {
		t.Fatalf("读取计数基线失败: %v", err)
	}
	return parseCountValue(values[0]), parseCountValue(values[1])
}

// TestActionCountSeedsFromStatsOnFirstAccess 验证首次访问用 pf_clip_stats 种子化 base，
// 本次点赞只让分片增量 +1，返回值 = 基线 100 + 增量 1 = 101。
func TestActionCountSeedsFromStatsOnFirstAccess(t *testing.T) {
	ctx := context.Background()
	stats := newFakeActionStats()
	const clipID int64 = 8001
	stats.set(clipID, 100, 7)
	cache, _ := newActionTestCache(t, WithActionStatsReader(stats))

	result, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, true)
	if err != nil {
		t.Fatalf("点赞失败: %v", err)
	}
	if !result.Changed || result.LikeCount != 101 || result.SaveCount != 7 {
		t.Fatalf("首次点赞结果 = %+v，期望 Changed=true LikeCount=101 SaveCount=7", result)
	}
	baseLike, baseSave := readActionBase(t, cache, clipID)
	if baseLike != 100 || baseSave != 7 {
		t.Fatalf("种子化基线 like=%d save=%d，期望 100/7", baseLike, baseSave)
	}
	if _, total := likeShardSums(t, cache, clipID); total != 1 {
		t.Fatalf("点赞分片增量 = %d，期望 1", total)
	}
}

// TestActionCountAccumulatesShardIncrements 验证多位读者的增量持续叠加在 DB 基线上，
// 重复点赞幂等且 save 与 like 分别计数。
func TestActionCountAccumulatesShardIncrements(t *testing.T) {
	ctx := context.Background()
	stats := newFakeActionStats()
	const clipID int64 = 8002
	stats.set(clipID, 100, 7)
	cache, _ := newActionTestCache(t, WithActionStatsReader(stats))

	for userID := int64(1); userID <= 3; userID++ {
		if _, err := cache.SetActionState(ctx, userID, clipID, dominteraction.ActionLike, true); err != nil {
			t.Fatalf("用户 %d 点赞失败: %v", userID, err)
		}
	}
	result, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionSave, true)
	if err != nil {
		t.Fatalf("收藏失败: %v", err)
	}
	if result.LikeCount != 103 || result.SaveCount != 8 {
		t.Fatalf("累加后 like=%d save=%d，期望 103/8", result.LikeCount, result.SaveCount)
	}

	again, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, true)
	if err != nil {
		t.Fatalf("重复点赞失败: %v", err)
	}
	if again.Changed || again.LikeCount != 103 {
		t.Fatalf("重复点赞 = %+v，期望 Changed=false 且 LikeCount=103", again)
	}
}

// TestActionCountUnlikeStopsAtZero 验证取消降到 0 后重复取消不再递减，
// 且状态位被置为有效但没有对应增量时也不会把计数压成负数。
func TestActionCountUnlikeStopsAtZero(t *testing.T) {
	ctx := context.Background()
	stats := newFakeActionStats()
	const clipID int64 = 8003
	cache, _ := newActionTestCache(t, WithActionStatsReader(stats))

	// 基线为 0：点赞后计数为 1，取消后回到 0。
	if result, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, true); err != nil || result.LikeCount != 1 {
		t.Fatalf("点赞结果 = %+v (err=%v)，期望 LikeCount=1", result, err)
	}
	if result, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, false); err != nil || result.LikeCount != 0 {
		t.Fatalf("取消点赞结果 = %+v (err=%v)，期望 LikeCount=0", result, err)
	}
	again, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, false)
	if err != nil {
		t.Fatalf("重复取消失败: %v", err)
	}
	if again.Changed || again.LikeCount != 0 {
		t.Fatalf("重复取消 = %+v，期望 Changed=false 且 LikeCount=0", again)
	}

	// 人为把状态位置为「已点赞」但没有分片增量：取消只能翻转状态，不能把计数减成负数。
	if err := cache.client.Set(ctx, actionStateKey(clipID, 2, dominteraction.ActionLike), "1", 0).Err(); err != nil {
		t.Fatalf("写入状态位失败: %v", err)
	}
	result, err := cache.SetActionState(ctx, 2, clipID, dominteraction.ActionLike, false)
	if err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if result.LikeCount != 0 {
		t.Fatalf("零下限保护失效：LikeCount=%d，期望 0", result.LikeCount)
	}
	shards, total := likeShardSums(t, cache, clipID)
	if total != 0 {
		t.Fatalf("点赞分片总和 = %d，期望 0（分片=%v）", total, shards)
	}
	for shard, value := range shards {
		if value < 0 {
			t.Fatalf("分片 %d 计数为负: %d", shard, value)
		}
	}
}

// TestActionCountReseedAfterBaseExpiryDoesNotDoubleCount 验证 base 单独过期而分片仍在时，
// 用 base = stats - Σ(shards) 重种子化：已落库的增量不会被 base 与分片重复计。
func TestActionCountReseedAfterBaseExpiryDoesNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	stats := newFakeActionStats()
	const clipID int64 = 8004
	stats.set(clipID, 100, 0)
	cache, _ := newActionTestCache(t, WithActionStatsReader(stats))

	// 5 位读者点赞：base=100、分片增量=5，读数 105。
	for userID := int64(1); userID <= 5; userID++ {
		if _, err := cache.SetActionState(ctx, userID, clipID, dominteraction.ActionLike, true); err != nil {
			t.Fatalf("用户 %d 点赞失败: %v", userID, err)
		}
	}
	// Worker 已把这 5 个增量落库：stats 变为 105，但分片仍保留 5。
	stats.set(clipID, 105, 0)

	// 只让 base 过期，模拟 base TTL 到期而分片尚未到期。
	if err := cache.client.Del(ctx, actionBaseKey(clipID)).Err(); err != nil {
		t.Fatalf("删除基线失败: %v", err)
	}

	// 第 6 位读者点赞前会触发重种子化：base = 105 - 5 = 100，再加本次增量得到 106。
	result, err := cache.SetActionState(ctx, 6, clipID, dominteraction.ActionLike, true)
	if err != nil {
		t.Fatalf("重种子化后点赞失败: %v", err)
	}
	if result.LikeCount != 106 {
		t.Fatalf("重种子化后 LikeCount=%d，期望 106（若把 base 直接写成 stats 会得到 111）", result.LikeCount)
	}
	if baseLike, baseSave := readActionBase(t, cache, clipID); baseLike != 100 || baseSave != 0 {
		t.Fatalf("重种子化基线 like=%d save=%d，期望 100/0", baseLike, baseSave)
	}
}

// TestActionCountReseedAfterTTLDoesNotDoubleCount 验证 base 与分片同时到期后重新种子化：
// 分片归零、base 取当前 DB 基线，读数与 DB 对齐且不重复计。
func TestActionCountReseedAfterTTLDoesNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	stats := newFakeActionStats()
	const clipID int64 = 8005
	stats.set(clipID, 100, 0)
	cache, server := newActionTestCache(t, WithActionStatsReader(stats))

	if result, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, true); err != nil || result.LikeCount != 101 {
		t.Fatalf("点赞结果 = %+v (err=%v)，期望 101", result, err)
	}
	// Worker 落库后 DB 基线为 101。
	stats.set(clipID, 101, 0)

	// 快进超过 24h：base 与分片一起过期，只剩无 TTL 的状态位。
	server.FastForward(25 * time.Hour)

	// 另一位读者点赞：重种子化 base=101（分片已为空），本次增量后为 102。
	result, err := cache.SetActionState(ctx, 2, clipID, dominteraction.ActionLike, true)
	if err != nil {
		t.Fatalf("TTL 到期后点赞失败: %v", err)
	}
	if result.LikeCount != 102 {
		t.Fatalf("TTL 到期后 LikeCount=%d，期望 102", result.LikeCount)
	}
	if baseLike, _ := readActionBase(t, cache, clipID); baseLike != 101 {
		t.Fatalf("TTL 到期后重种子化基线 like=%d，期望 101", baseLike)
	}
	if _, total := likeShardSums(t, cache, clipID); total != 1 {
		t.Fatalf("TTL 到期后分片增量 = %d，期望 1", total)
	}
}

// TestActionCountNeverNegativeOnReseed 验证分片增量大于 DB 基线时（未落库增量），
// base 可为负补偿值，但对外读数仍钳制为 0。
func TestActionCountNeverNegativeOnReseed(t *testing.T) {
	ctx := context.Background()
	stats := newFakeActionStats()
	const clipID int64 = 8006
	cache, _ := newActionTestCache(t, WithActionStatsReader(stats))

	// 基线 0，点赞后分片为 1。
	if _, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, true); err != nil {
		t.Fatalf("点赞失败: %v", err)
	}
	// 基线过期后重新种子化：base = 0 - 1 = -1，合计仍为 0，不得返回负数。
	if err := cache.client.Del(ctx, actionBaseKey(clipID)).Err(); err != nil {
		t.Fatalf("删除基线失败: %v", err)
	}
	result, err := cache.SetActionState(ctx, 1, clipID, dominteraction.ActionLike, true)
	if err != nil {
		t.Fatalf("幂等点赞失败: %v", err)
	}
	if result.Changed {
		t.Fatalf("重复点赞不应改变状态: %+v", result)
	}
	if result.LikeCount != 0 {
		t.Fatalf("合计钳制失效：LikeCount=%d，期望 0", result.LikeCount)
	}
	if baseLike, _ := readActionBase(t, cache, clipID); baseLike != -1 {
		t.Fatalf("补偿基线 like=%d，期望 -1", baseLike)
	}
}
