//go:build integration

// following_overflow_integration_test.go 用真实 MySQL+Redis 验证 following 流在
// inbox 被 1000 条裁剪后，仍能通过 DB 回源翻页读到更早内容，且 HasMore 与游标正确。
package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	appfeed "pulsefeed/internal/application/feed"
	domclip "pulsefeed/internal/domain/clip"
	domfeed "pulsefeed/internal/domain/feed"
	domrelation "pulsefeed/internal/domain/relation"
	"pulsefeed/internal/infra/cache"
	infrafeed "pulsefeed/internal/infra/persistence/feed"
	infrarelation "pulsefeed/internal/infra/persistence/relation"
)

// TestFollowingPagingRecoversTrimmedOverflow 写入超过裁剪上限的作品后只把最新 1000 条放进 inbox，
// 逐页翻到底应覆盖全部作品（含被裁剪的最旧 5 条），证明缓存耗尽后回源数据库兜住了漏读。
func TestFollowingPagingRecoversTrimmedOverflow(t *testing.T) {
	ctx := context.Background()
	redisAddr := envOr("PULSEFEED_IT_REDIS_ADDR", defaultRedis)
	client := redis.NewClient(&redis.Options{Addr: redisAddr, DB: 15})
	defer client.Close()

	suffix := time.Now().UnixNano() % 1_000_000
	viewerID := int64(900_000_000 + suffix)
	creatorID := int64(910_000_000 + suffix)

	const total = 1005
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	entries := make([]domfeed.InboxEntry, 0, total)
	clipIDs := make([]int64, 0, total)
	for i := 1; i <= total; i++ {
		publishedAt := base.Add(time.Duration(i) * time.Second)
		clipID := seedClip(t, creatorID, fmt.Sprintf("overflow-%d-%d", suffix, i), publishedAt, domclip.StatusPublished)
		clipIDs = append(clipIDs, clipID)
		entries = append(entries, domfeed.InboxEntry{ClipID: clipID, CreatorID: creatorID, PublishedAt: publishedAt})
	}

	if err := testDB.Create(&infrarelation.FollowModel{
		UserID: viewerID, TargetUserID: creatorID, Status: domrelation.StatusFollowing,
		IdempotencyKey: uniqueKey(fmt.Sprintf("follow-%d", suffix)),
	}).Error; err != nil {
		t.Fatalf("写入关注关系失败: %v", err)
	}
	if err := testDB.Create(&infrarelation.UserStatsModel{UserID: creatorID, FollowerCount: 1}).Error; err != nil {
		t.Fatalf("写入用户统计失败: %v", err)
	}

	feedCache := cache.NewFeedCache(client)
	if err := feedCache.AddInboxEntries(ctx, viewerID, entries); err != nil {
		t.Fatalf("写入 inbox 失败: %v", err)
	}
	inboxKey := fmt.Sprintf("pf:v1:inbox:%d", viewerID)
	defer client.Del(ctx, inboxKey)
	if size, err := client.ZCard(ctx, inboxKey).Result(); err != nil || size != 1000 {
		t.Fatalf("inbox 应被裁剪为 1000 条，实际 %d (err=%v)", size, err)
	}

	repo := infrafeed.New(testDB)
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(feedCache))

	seen := make(map[int64]bool, total)
	cursor := ""
	for page := 0; ; page++ {
		if page > 100 {
			t.Fatalf("翻页未收敛，已读取 %d 条", len(seen))
		}
		result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: viewerID, Cursor: cursor, Limit: 50})
		if err != nil {
			t.Fatalf("读取关注流失败: %v", err)
		}
		for _, item := range result.Items {
			if seen[item.ClipID] {
				t.Fatalf("翻页出现重复条目 clip=%d", item.ClipID)
			}
			seen[item.ClipID] = true
		}
		if !result.HasMore {
			break
		}
		if result.NextCursor == "" {
			t.Fatalf("HasMore=true 但游标为空")
		}
		cursor = result.NextCursor
	}

	if len(seen) != total {
		t.Fatalf("应翻到全部 %d 条，实际 %d 条", total, len(seen))
	}
	for i := 0; i < 5; i++ {
		if !seen[clipIDs[i]] {
			t.Fatalf("被缓存裁剪的最旧作品 clip=%d 未被回源补齐", clipIDs[i])
		}
	}
}
