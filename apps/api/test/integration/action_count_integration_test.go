//go:build integration

// action_count_integration_test.go 用真实 MySQL + Redis 复现「分片计数永远从 0 累加」的原始缺陷：
// DB 里 like_count=100、Redis 为空时点赞，接口返回值与 DB 都必须是 101；
// 再次清空 Redis（模拟重启/缓存丢失）后继续点赞，计数仍从 DB 基线出发保持对齐。
// 依赖 setup_integration_test.go 的独立 schema 与 Redis DB 15。
package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appaction "pulsefeed/internal/application/interaction"
	infracache "pulsefeed/internal/infra/cache"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infrafeed "pulsefeed/internal/infra/persistence/feed"
	infraaction "pulsefeed/internal/infra/persistence/interaction"

	redis "github.com/redis/go-redis/v9"
)

// syncActionPublisher 把 action_changed 事件同步交给 Worker 处理，
// 在进程内等价复现「Redis 先写 → MQ → Worker 落库」的异步链路，无需等待真实 MQ。
type syncActionPublisher struct {
	worker *appaction.ActionWorker
}

// PublishActionChanged 实现事件投递端口。
func (p syncActionPublisher) PublishActionChanged(ctx context.Context, event appaction.ActionChanged) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.worker.Handle(ctx, body)
}

// TestActionCountSeedsFromDBStats 验证计数缓存以 pf_clip_stats 为基线，Redis 为空时不再从 0 起算。
func TestActionCountSeedsFromDBStats(t *testing.T) {
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{
		Addr: envOr("PULSEFEED_IT_REDIS_ADDR", defaultRedis),
		DB:   15,
	})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("连接 Redis 失败: %v", err)
	}
	// 清空本测试库，确保 Redis 里没有任何计数键，复现「缓存为空 + DB 有历史计数」的现场。
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("清空 Redis 失败: %v", err)
	}

	const creatorID int64 = 9201
	clipID := seedClip(t, creatorID, "it-action-count", time.Now(), 1)
	if err := testDB.Create(&infraclip.ClipStatsModel{ClipID: clipID, LikeCount: 100}).Error; err != nil {
		t.Fatalf("写入计数基线失败: %v", err)
	}
	// 本用例的 clip 发布在当前时刻，会出现在 latest 首页；跑完即删，避免污染分页等共享 schema 的用例。
	t.Cleanup(func() {
		testDB.Delete(&infraclip.ClipStatsModel{}, "clip_id = ?", clipID)
		testDB.Delete(&infraclip.ClipModel{}, "id = ?", clipID)
	})

	repo := infraaction.New(testDB)
	cache := infracache.NewFeedCache(client, infracache.WithActionStatsReader(infrafeed.New(testDB)))
	publisher := syncActionPublisher{worker: appaction.NewActionWorker(repo)}
	service := appaction.New(repo, appaction.WithAsyncActionPipeline(cache, publisher))

	readDBLikeCount := func() int64 {
		t.Helper()
		var stats infraclip.ClipStatsModel
		if err := testDB.Take(&stats, "clip_id = ?", clipID).Error; err != nil {
			t.Fatalf("读取 pf_clip_stats 失败: %v", err)
		}
		return stats.LikeCount
	}

	// 第一次点赞：接口返回基线 100 + 增量 1 = 101，Worker 落库后 DB 也是 101。
	result, err := service.Like(ctx, 9211, clipID, uniqueKey("it-action-like-1"))
	if err != nil {
		t.Fatalf("第一次点赞失败: %v", err)
	}
	if result.LikeCount != 101 {
		t.Fatalf("第一次点赞返回 LikeCount=%d，期望 101（Redis 为空时未按 DB 基线种子化）", result.LikeCount)
	}
	if dbLike := readDBLikeCount(); dbLike != 101 {
		t.Fatalf("第一次点赞后 DB like_count=%d，期望 101", dbLike)
	}

	// 再次清空 Redis，模拟重启/缓存丢失：下一次点赞仍应得到 102，而不是从 0 重新开始。
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("再次清空 Redis 失败: %v", err)
	}
	result, err = service.Like(ctx, 9212, clipID, uniqueKey("it-action-like-2"))
	if err != nil {
		t.Fatalf("清空 Redis 后点赞失败: %v", err)
	}
	if result.LikeCount != 102 {
		t.Fatalf("清空 Redis 后 LikeCount=%d，期望 102（应重新以 DB 基线种子化）", result.LikeCount)
	}
	if dbLike := readDBLikeCount(); dbLike != 102 {
		t.Fatalf("清空 Redis 后 DB like_count=%d，期望 102", dbLike)
	}

	t.Logf("计数单一真相验收通过：DB 基线 100 → 点赞 101 → 清空 Redis → 点赞 102，接口与 DB 始终一致")
}
