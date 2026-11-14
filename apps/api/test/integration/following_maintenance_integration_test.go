//go:build integration

// following_maintenance_integration_test.go 覆盖 following 流本次改造的真机集成用例：
// 毫秒 score 往返与按毫秒游标翻页、取关清理 inbox 残留、大 V outbox 批量 pipeline，
// 以及关注上限在真实数据库上的拒写行为。所有 Redis 键用独立 viewer/creator ID 并在结束时删除，
// 数据库使用 TestMain 独占 schema，跑完自动 DROP，不污染开发环境。
package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	apprelation "pulsefeed/internal/application/relation"
	domfeed "pulsefeed/internal/domain/feed"
	domrelation "pulsefeed/internal/domain/relation"
	infracache "pulsefeed/internal/infra/cache"
	infraaccount "pulsefeed/internal/infra/persistence/account"
	infrarelation "pulsefeed/internal/infra/persistence/relation"
)

// seedITUsers 写入测试用户；关注关系要求目标用户在 pf_users 中存在。
func seedITUsers(t *testing.T, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		model := &infraaccount.UserModel{
			ID:           id,
			Account:      fmt.Sprintf("it-%d", id),
			PasswordHash: "x",
			Status:       1,
		}
		if err := testDB.Create(model).Error; err != nil {
			t.Fatalf("写入用户 %d 失败: %v", id, err)
		}
	}
}

// newITRedis 创建指向集成 Redis（默认 DB 15）的客户端，并在用例结束时关闭。
func newITRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr: envOr("PULSEFEED_IT_REDIS_ADDR", defaultRedis),
		DB:   15,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// itID 返回本次运行内不冲突的测试 ID 基数。
func itID() int64 {
	return int64(time.Now().UnixNano()%1_000_000) * 10
}

// TestFeedBoxMillisecondScorePagingAndCleanup 验证真实 Redis 下：
// 写入分值为毫秒、毫秒游标能精确翻到同一秒内更早的条目、取关清理只删除目标创作者的精确成员。
func TestFeedBoxMillisecondScorePagingAndCleanup(t *testing.T) {
	ctx := context.Background()
	client := newITRedis(t)
	cache := infracache.NewFeedCache(client)

	viewerID := 900_000_000 + itID()
	creatorA := int64(770001)
	creatorB := int64(770002)
	key := fmt.Sprintf("pf:v2:feed:inbox:%d", viewerID)
	t.Cleanup(func() { client.Del(context.Background(), key) })

	base := time.Now().Truncate(time.Second)
	entries := []domfeed.InboxEntry{
		{ClipID: 1, CreatorID: creatorA, PublishedAt: base},
		{ClipID: 2, CreatorID: creatorA, PublishedAt: base.Add(5 * time.Millisecond)},
		{ClipID: 3, CreatorID: creatorB, PublishedAt: base.Add(10 * time.Millisecond)},
	}
	if err := cache.AddInboxEntries(ctx, viewerID, entries); err != nil {
		t.Fatalf("写入 inbox 失败: %v", err)
	}

	// 真实 Redis 中的 score 必须是毫秒，而不是秒。
	for _, entry := range entries {
		member := fmt.Sprintf("%d:%d", entry.ClipID, entry.CreatorID)
		score, err := client.ZScore(ctx, key, member).Result()
		if err != nil {
			t.Fatalf("读取 %s 分值失败: %v", member, err)
		}
		if int64(score) != entry.PublishedAt.UnixMilli() {
			t.Fatalf("%s 分值应为毫秒 %d，实际 %d", member, entry.PublishedAt.UnixMilli(), int64(score))
		}
	}

	// 游标停在 5ms，翻页应只返回同一秒内更早的 0ms 条目，而不是重读整秒。
	page, err := cache.ListInbox(ctx, viewerID, &domfeed.Cursor{PublishedAt: entries[1].PublishedAt, ClipID: 2}, 10)
	if err != nil {
		t.Fatalf("按毫秒游标翻页失败: %v", err)
	}
	if len(page) != 1 || page[0].ClipID != 1 || !page[0].PublishedAt.Equal(base) {
		t.Fatalf("毫秒游标翻页结果不正确: %+v", page)
	}

	// 取关清理：只删 creatorA 的 2 条精确成员，creatorB 保留。
	if err := cache.RemoveCreatorFromInbox(ctx, viewerID, creatorA); err != nil {
		t.Fatalf("取关清理失败: %v", err)
	}
	rest, err := cache.ListInbox(ctx, viewerID, nil, 10)
	if err != nil {
		t.Fatalf("清理后读取失败: %v", err)
	}
	if len(rest) != 1 || rest[0].CreatorID != creatorB {
		t.Fatalf("清理后应只剩 creatorB 的条目，实际 %+v", rest)
	}
}

// TestFeedOutboxBatchReadAcrossPipelineChunks 验证 120 个大 V 源跨两个 pipeline 分片批量读取：
// 真实 Redis 下一次调用返回全部源的最新条目，每个源最多 limit 条。
func TestFeedOutboxBatchReadAcrossPipelineChunks(t *testing.T) {
	ctx := context.Background()
	client := newITRedis(t)
	cache := infracache.NewFeedCache(client)

	const creatorCount = 120
	base := time.Now().Truncate(time.Second)
	creatorIDs := make([]int64, 0, creatorCount)
	keys := make([]string, 0, creatorCount)
	for i := 0; i < creatorCount; i++ {
		creatorID := int64(760000 + i)
		creatorIDs = append(creatorIDs, creatorID)
		keys = append(keys, fmt.Sprintf("pf:v2:feed:outbox:%d", creatorID))
		entries := []domfeed.InboxEntry{
			{ClipID: int64(i*2 + 1), CreatorID: creatorID, PublishedAt: base.Add(time.Duration(i) * time.Second)},
			{ClipID: int64(i*2 + 2), CreatorID: creatorID, PublishedAt: base.Add(time.Duration(i)*time.Second + time.Millisecond)},
		}
		if err := cache.AddOutboxEntries(ctx, creatorID, entries); err != nil {
			t.Fatalf("写入 outbox 失败: %v", err)
		}
	}
	t.Cleanup(func() { client.Del(context.Background(), keys...) })

	entries, err := cache.ListOutboxes(ctx, creatorIDs, nil, 1)
	if err != nil {
		t.Fatalf("批量读取 outbox 失败: %v", err)
	}
	if len(entries) != creatorCount {
		t.Fatalf("每个源应各返回 1 条，期望 %d 条，实际 %d 条", creatorCount, len(entries))
	}
	seen := make(map[int64]bool, creatorCount)
	for _, entry := range entries {
		if seen[entry.CreatorID] {
			t.Fatalf("同一源不应重复返回: creator=%d", entry.CreatorID)
		}
		seen[entry.CreatorID] = true
	}
}

// TestFollowLimitRejectsAtMaxFollowingCount 验证关注数达到上限时真实数据库上拒绝新增关注，
// 且不会写出任何关注关系。
func TestFollowLimitRejectsAtMaxFollowingCount(t *testing.T) {
	ctx := context.Background()
	viewerID := 910_000_000 + itID()
	targetID := viewerID + 1

	stats := &infrarelation.UserStatsModel{
		UserID:         viewerID,
		FollowingCount: domrelation.MaxFollowingCount,
		FollowerCount:  0,
	}
	if err := testDB.Create(stats).Error; err != nil {
		t.Fatalf("写入用户统计失败: %v", err)
	}

	svc := apprelation.New(infrarelation.New(testDB))
	if _, err := svc.Follow(ctx, viewerID, targetID, uniqueKey("follow-limit")); !errors.Is(err, domrelation.ErrFollowingLimit) {
		t.Fatalf("达到上限应返回 ErrFollowingLimit，实际 %v", err)
	}

	var count int64
	if err := testDB.Table("pf_follows").
		Where("user_id = ? AND target_user_id = ?", viewerID, targetID).
		Count(&count).Error; err != nil {
		t.Fatalf("查询关注关系失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("超限关注不应落库，实际写入 %d 条", count)
	}
}

// TestUnfollowCleansInboxResidueEndToEnd 验证真实仓储 + 真实 Redis 下取关会清理读者 inbox
// 中该创作者的全部残留，同时保留其他创作者的条目。
func TestUnfollowCleansInboxResidueEndToEnd(t *testing.T) {
	ctx := context.Background()
	client := newITRedis(t)
	cache := infracache.NewFeedCache(client)
	repo := infrarelation.New(testDB)

	viewerID := 920_000_000 + itID()
	creatorA := viewerID + 1
	creatorB := viewerID + 2
	key := fmt.Sprintf("pf:v2:feed:inbox:%d", viewerID)
	t.Cleanup(func() { client.Del(context.Background(), key) })

	seedITUsers(t, viewerID, creatorA, creatorB)
	if _, err := repo.SetFollow(ctx, viewerID, creatorA, true, uniqueKey("follow-a")); err != nil {
		t.Fatalf("预置关注 A 失败: %v", err)
	}
	if _, err := repo.SetFollow(ctx, viewerID, creatorB, true, uniqueKey("follow-b")); err != nil {
		t.Fatalf("预置关注 B 失败: %v", err)
	}

	base := time.Now().Truncate(time.Second)
	entries := []domfeed.InboxEntry{
		{ClipID: 11, CreatorID: creatorA, PublishedAt: base.Add(3 * time.Second)},
		{ClipID: 12, CreatorID: creatorB, PublishedAt: base.Add(2 * time.Second)},
		{ClipID: 13, CreatorID: creatorA, PublishedAt: base.Add(time.Second)},
	}
	if err := cache.AddInboxEntries(ctx, viewerID, entries); err != nil {
		t.Fatalf("写入 inbox 失败: %v", err)
	}

	svc := apprelation.New(repo, apprelation.WithInboxCleaner(cache))
	result, err := svc.Unfollow(ctx, viewerID, creatorA, uniqueKey("unfollow-a"))
	if err != nil {
		t.Fatalf("取关失败: %v", err)
	}
	if !result.Changed || result.Following {
		t.Fatalf("取关回执不正确: %+v", result)
	}

	rest, err := cache.ListInbox(ctx, viewerID, nil, 10)
	if err != nil {
		t.Fatalf("清理后读取 inbox 失败: %v", err)
	}
	if len(rest) != 1 || rest[0].CreatorID != creatorB {
		t.Fatalf("取关后应只剩 creatorB 的条目，实际 %+v", rest)
	}
}
