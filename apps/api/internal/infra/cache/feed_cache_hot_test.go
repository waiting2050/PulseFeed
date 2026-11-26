// feed_cache_hot_test.go 用进程内 miniredis 验证热度分钟桶语义：
// 同一分钟多次写入按 ZINCRBY 累加而非覆盖；跨分钟写入落到新桶、旧桶保持原值；
// 旧桶按 TTL 过期且新桶分数不被旧桶污染，窗口合并按桶求和不串分；
// pipeline 读取与逐桶串行的合并结果一致，部分桶失败语义保持不变。
package cache

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	domfeed "pulsefeed/internal/domain/feed"

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

// TestHotBucketsAcceptNegativeDeltaAndFilterNonPositive 验证取消操作会写入负分，
// 但累计非正的条目不会进入热榜窗口；非法 clip 与零增量仍不写桶。
func TestHotBucketsAcceptNegativeDeltaAndFilterNonPositive(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	cache, server := newHotTestCache(t, func() time.Time { return now })

	cache.RecordHotScore(ctx, 0, 5)
	cache.RecordHotScore(ctx, -1, 5)
	cache.RecordHotScore(ctx, 101, 0)

	if server.Exists(hotBucketKey(now)) {
		t.Fatalf("非法 clip 与零增量不应创建热度分钟桶")
	}

	// 点赞后取消：桶内累计为 0，热榜窗口应剔除。
	cache.RecordHotScore(ctx, 101, 3)
	cache.RecordHotScore(ctx, 101, -3)
	// 仅取消、从未点赞的条目：桶内为负分，同样不占位。
	cache.RecordHotScore(ctx, 202, -2)

	if score, err := server.ZScore(hotBucketKey(now), "101"); err != nil || score != 0 {
		t.Fatalf("clip 101 桶内分数 = %v (err=%v), 期望 0（+3-3）", score, err)
	}
	items, err := cache.ListHotWindow(ctx)
	if err != nil {
		t.Fatalf("读取热度窗口失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("非正分条目不应进入热榜窗口，实际 %+v", items)
	}
}

// serialHotWindow 用逐桶串行 ZRANGE 复算窗口（pipeline 改造前的读法），
// 作为 ListHotWindow 的对照基准，验证两种读法合并、排序、去重结果完全一致。
func serialHotWindow(t *testing.T, ctx context.Context, client *redis.Client, now time.Time) []domfeed.HotItem {
	t.Helper()
	acc := make(map[string]float64, hotWindowLimit)
	for i := 0; i < hotWindowBuckets; i++ {
		key := hotBucketKey(now.Add(-time.Duration(i) * time.Minute))
		values, err := client.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			t.Fatalf("串行读取分钟桶失败 (key=%s): %v", key, err)
		}
		for _, value := range values {
			member, ok := value.Member.(string)
			if !ok {
				continue
			}
			acc[member] += value.Score
		}
	}
	items := make([]domfeed.HotItem, 0, len(acc))
	for member, score := range acc {
		if score <= 0 {
			continue
		}
		clipID, err := strconv.ParseInt(member, 10, 64)
		if err != nil {
			continue
		}
		items = append(items, domfeed.HotItem{ClipID: clipID, Score: score})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ClipID > items[j].ClipID
	})
	if len(items) > hotWindowLimit {
		items = items[:hotWindowLimit]
	}
	return items
}

// TestListHotWindowPipelineMatchesSerial 验证 pipeline 读取的合并、排序、去重与串行逐桶读取一致。
func TestListHotWindowPipelineMatchesSerial(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	cache, server := newHotTestCache(t, func() time.Time { return now })

	// 三个桶：跨桶累加（101）、桶内同分、负分（505）、非数字成员（not-a-clip）混排。
	buckets := []struct {
		offset int
		values map[string]float64
	}{
		{0, map[string]float64{"101": 3, "202": 5, "303": 2}},
		{1, map[string]float64{"101": 4, "404": 9, "505": -1}},
		{2, map[string]float64{"101": -2, "202": 2, "303": 1, "606": 1, "not-a-clip": 6}},
	}
	for _, bucket := range buckets {
		key := hotBucketKey(now.Add(-time.Duration(bucket.offset) * time.Minute))
		for member, score := range bucket.values {
			if _, err := server.ZAdd(key, score, member); err != nil {
				t.Fatalf("预置分钟桶失败 (key=%s member=%s): %v", key, member, err)
			}
		}
	}

	got, err := cache.ListHotWindow(ctx)
	if err != nil {
		t.Fatalf("读取热度窗口失败: %v", err)
	}
	if want := serialHotWindow(t, ctx, cache.client, now); !reflect.DeepEqual(got, want) {
		t.Fatalf("pipeline 读取结果与串行不一致:\n pipeline=%+v\n serial  =%+v", got, want)
	}

	// 显式断言合并结果，避免两种读法被同步改错：101=3+4-2=5、202=5+2=7、303=3、404=9、606=1，
	// 505 负分与非数字成员均被剔除；同分时按 clip ID 递减。
	want := []domfeed.HotItem{
		{ClipID: 404, Score: 9},
		{ClipID: 202, Score: 7},
		{ClipID: 101, Score: 5},
		{ClipID: 303, Score: 3},
		{ClipID: 606, Score: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("热搜窗口合并结果 = %+v, 期望 %+v", got, want)
	}
}

// TestListHotWindowPipelineKeepsPartialFailureSemantics 验证 pipeline 下单个桶读取失败
// 不影响其余桶合并；全部桶失败或失败且无数据时仍返回 ErrHotWindowUnavailable。
func TestListHotWindowPipelineKeepsPartialFailureSemantics(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	cache, server := newHotTestCache(t, func() time.Time { return now })

	// 一个桶被写成字符串类型：该桶 ZRANGE 返回 WRONGTYPE，其余桶正常可读。
	broken := hotBucketKey(now.Add(-time.Minute))
	if err := server.Set(broken, "not-a-zset"); err != nil {
		t.Fatalf("预置异常桶失败: %v", err)
	}
	good := hotBucketKey(now)
	if _, err := server.ZAdd(good, 4, "101"); err != nil {
		t.Fatalf("预置正常桶失败: %v", err)
	}

	items, err := cache.ListHotWindow(ctx)
	if err != nil {
		t.Fatalf("部分桶失败但仍有数据时应继续返回: %v", err)
	}
	if len(items) != 1 || items[0].ClipID != 101 || items[0].Score != 4 {
		t.Fatalf("应只合并正常桶的数据，实际 %+v", items)
	}

	// 只剩失败桶、没有任何可用数据：无法区分空窗口与读失败，必须报可用性错误。
	server.Del(good)
	if _, err := cache.ListHotWindow(ctx); !errors.Is(err, ErrHotWindowUnavailable) {
		t.Fatalf("失败且无数据时应返回 ErrHotWindowUnavailable，实际 %v", err)
	}
}
