//go:build integration

// delete_cache_integration_test.go 覆盖删除作品后的 Feed 缓存失效：
// 预热 latest 页缓存并写入卡片/计数缓存 → 删除 → 页缓存版本递增、卡片与计数键被删，
// 再次读取 latest 不再包含该 clip。
// 依赖真实 MySQL 与 Redis（DB 15），复用 setup_integration_test.go 的独立 schema。
package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	appclip "pulsefeed/internal/application/clip"
	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
	infracache "pulsefeed/internal/infra/cache"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infrafeed "pulsefeed/internal/infra/persistence/feed"

	redis "github.com/redis/go-redis/v9"
)

// TestDeleteInvalidatesFeedCache 验证删除后页缓存、卡片缓存与计数缓存立即失效。
func TestDeleteInvalidatesFeedCache(t *testing.T) {
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{
		Addr: envOr("PULSEFEED_IT_REDIS_ADDR", defaultRedis),
		DB:   15,
	})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("连接 Redis 失败: %v", err)
	}
	cache := infracache.NewFeedCache(client)

	const creatorID int64 = 9101
	clipID := seedClip(t, creatorID, "it-delete-cache", time.Now(), 1)

	clipService := appclip.New(infraclip.New(testDB), appclip.WithFeedCacheInvalidator(cache))
	feedService := appfeed.New(infrafeed.New(testDB), appfeed.WithFeedCache(cache))
	req := &appfeed.FeedRequest{Scene: string(domfeed.SceneLatest), Limit: 20}

	assertContains := func(stage string) {
		t.Helper()
		result, err := feedService.GetFeed(ctx, req)
		if err != nil {
			t.Fatalf("%s读取 latest 失败: %v", stage, err)
		}
		for _, item := range result.Items {
			if item.ClipID == clipID {
				return
			}
		}
		t.Fatalf("%s latest 未包含 clip %d", stage, clipID)
	}

	// 首次读取回源并回填页缓存，第二次读取应命中同一份缓存。
	assertContains("预热前")
	assertContains("预热后")

	// 单独写入卡片与计数缓存，验证删除会显式 DEL 两个键。
	cache.SetCards(ctx, []*domfeed.FeedEntry{{
		ClipID:    clipID,
		CreatorID: creatorID,
		Title:     "it-delete-cache",
	}})
	cache.SetStats(ctx, map[int64]*domfeed.EntryStats{clipID: {}})
	cardKey := fmt.Sprintf("pf:v1:feed:card:%d", clipID)
	statsKey := fmt.Sprintf("pf:v1:feed:stats:%d", clipID)
	if n, err := client.Exists(ctx, cardKey, statsKey).Result(); err != nil || n != 2 {
		t.Fatalf("预热缓存键失败: exists=%d err=%v", n, err)
	}

	versionBefore := cache.FeedVersion(ctx)
	if err := clipService.Delete(ctx, creatorID, clipID, uniqueKey("it-delete")); err != nil {
		t.Fatalf("删除 clip 失败: %v", err)
	}
	versionAfter := cache.FeedVersion(ctx)
	if versionAfter == versionBefore {
		t.Fatalf("删除后页缓存版本应递增，前后均为 %q", versionBefore)
	}

	if n, err := client.Exists(ctx, cardKey, statsKey).Result(); err != nil || n != 0 {
		t.Fatalf("删除后卡片/计数缓存应被清除: exists=%d err=%v", n, err)
	}

	// 页缓存已因版本递增失效，再次读取必须回源 DB 且不再包含已删除 clip。
	result, err := feedService.GetFeed(ctx, req)
	if err != nil {
		t.Fatalf("删除后读取 latest 失败: %v", err)
	}
	for _, item := range result.Items {
		if item.ClipID == clipID {
			t.Fatalf("删除后 latest 仍返回 clip %d，页缓存未失效", clipID)
		}
	}

	t.Logf("删除缓存失效验收通过：版本 %q→%q，卡片/计数键已删除，latest 不再包含 %d",
		versionBefore, versionAfter, clipID)
}
