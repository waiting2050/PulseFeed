// feed_cache_hot_test.go 用进程内 miniredis 验证热度分钟桶语义：
// 同一分钟多次写入按 ZINCRBY 累加而非覆盖；跨分钟写入落到新桶、旧桶保持原值；
// 旧桶按 TTL 过期且新桶分数不被旧桶污染，窗口合并按桶求和不串分。
package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newHotTestCache 启动进程内 Redis 并返回注入固定时钟的 FeedCache，测试结束自动回收。
func newHotTestCache(t *testing.T, clock func() time.Time) (*FeedCache, *miniredis.Miniredis) {
	t.Helper()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return &FeedCache{client: client, now: clock}, server
}

// TestHotBucketsAccumulateWithinMinuteAndRollOverAcrossMinutes 验证分钟桶的同分钟累加、
// 跨分钟分桶不串与旧桶 TTL 过期。
func TestHotBucketsAccumulateWithinMinuteAndRollOverAcrossMinutes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	cache, server := newHotTestCache(t, func() time.Time { return now })

	oldBucket := hotBucketKey(now)
	cache.RecordHotScore(ctx, 101, 2)
	cache.RecordHotScore(ctx, 101, 3)

	// 同一分钟内两次写入必须是累加（2+3=5），不能是覆盖（3）。
	if score, err := server.ZScore(oldBucket, "101"); err != nil || score != 5 {
		t.Fatalf("同分钟两次写入后旧桶 clip 101 分数 = %v (err=%v), 期望累加 5", score, err)
	}
	if ttl := server.TTL(oldBucket); ttl != hotBucketTTL {
		t.Fatalf("分钟桶 TTL = %v, 期望 %v", ttl, hotBucketTTL)
	}

	// 跨分钟边界：Go 时钟与 miniredis 时钟同步前进 70 秒，落到下一个分钟桶。
	server.FastForward(70 * time.Second)
	now = time.Date(2026, 1, 2, 15, 5, 10, 0, time.UTC)
	newBucket := hotBucketKey(now)
	if newBucket == oldBucket {
		t.Fatalf("跨分钟后桶键未变化: %s", newBucket)
	}

	cache.RecordHotScore(ctx, 101, 4)
	cache.RecordHotScore(ctx, 202, 7)

	// 旧桶保留 5 分不被覆盖；新桶独立计入 101=4、202=7，分数不串。
	if score, err := server.ZScore(oldBucket, "101"); err != nil || score != 5 {
		t.Fatalf("跨分钟后旧桶 clip 101 分数 = %v (err=%v), 期望保持 5", score, err)
	}
	if score, err := server.ZScore(newBucket, "101"); err != nil || score != 4 {
		t.Fatalf("新桶 clip 101 分数 = %v (err=%v), 期望 4（不得继承旧桶 5）", score, err)
	}
	if score, err := server.ZScore(newBucket, "202"); err != nil || score != 7 {
		t.Fatalf("新桶 clip 202 分数 = %v (err=%v), 期望 7", score, err)
	}

	// 窗口合并：两个桶都在最近 60 分钟内，101 应按桶求和为 9，202 为 7。
	items, err := cache.ListHotWindow(ctx)
	if err != nil {
		t.Fatalf("读取热度窗口失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("热度窗口条目数 = %d, 期望 2: %+v", len(items), items)
	}
	if items[0].ClipID != 101 || items[0].Score != 9 {
		t.Fatalf("热度窗口第 1 条 = %+v, 期望 clip 101 分数 9（5+4）", items[0])
	}
	if items[1].ClipID != 202 || items[1].Score != 7 {
		t.Fatalf("热度窗口第 2 条 = %+v, 期望 clip 202 分数 7", items[1])
	}

	// 旧桶在 65 分钟 TTL 后过期；新桶晚 70 秒写入仍在有效期内，分数保持 4。
	server.FastForward(64*time.Minute + 50*time.Second)
	if server.Exists(oldBucket) {
		t.Fatalf("旧桶 %s 已超过 TTL 仍存在", oldBucket)
	}
	if !server.Exists(newBucket) {
		t.Fatalf("新桶 %s 仍在 TTL 内却被删除", newBucket)
	}
	if score, err := server.ZScore(newBucket, "101"); err != nil || score != 4 {
		t.Fatalf("旧桶过期后新桶 clip 101 分数 = %v (err=%v), 期望仍为 4", score, err)
	}
}

// TestHotBucketsIgnoreInvalidInput 验证非正 clip 与非正增量不写桶，避免脏数据进入热榜。
func TestHotBucketsIgnoreInvalidInput(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	cache, server := newHotTestCache(t, func() time.Time { return now })

	cache.RecordHotScore(ctx, 0, 5)
	cache.RecordHotScore(ctx, -1, 5)
	cache.RecordHotScore(ctx, 101, 0)
	cache.RecordHotScore(ctx, 101, -2)

	if server.Exists(hotBucketKey(now)) {
		t.Fatalf("非法输入不应创建热度分钟桶")
	}
}
