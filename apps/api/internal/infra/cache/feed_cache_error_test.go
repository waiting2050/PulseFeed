// feed_cache_error_test.go 用进程内 miniredis 验证「缓存故障」与「未命中」可区分：
// 正常未命中返回 ok=false/err=nil，Redis 不可用后各读接口返回错误而不是静默当未命中，
// 热度窗口全桶读取失败不再返回空列表配 nil 错误。
package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newFaultTestCache 启动 miniredis 并返回缓存与可随时关闭的服务端；
// 客户端关闭重试避免故障用例被重试退避拖慢。
func newFaultTestCache(t *testing.T) (*FeedCache, *miniredis.Miniredis) {
	t.Helper()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{
		Addr:         server.Addr(),
		MaxRetries:   -1,
		DialTimeout:  200 * time.Millisecond,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		PoolTimeout:  200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	return &FeedCache{client: client}, server
}

// TestFeedCacheMissKeepsNilError 验证真正的未命中仍然是 ok=false/err=nil，不受故障改造影响。
func TestFeedCacheMissKeepsNilError(t *testing.T) {
	ctx := context.Background()
	cache, _ := newFaultTestCache(t)

	if _, ok, err := cache.GetPage(ctx, "digest"); ok || err != nil {
		t.Fatalf("页缓存未命中应返回 ok=false, err=nil: ok=%v err=%v", ok, err)
	}
	version, err := cache.FeedVersion(ctx)
	if err != nil || version != feedVersionDefault {
		t.Fatalf("版本键缺失应返回默认版本且无错误: version=%q err=%v", version, err)
	}
	if cards, err := cache.GetCards(ctx, []int64{1, 2}); err != nil || len(cards) != 0 {
		t.Fatalf("卡片未命中应返回空集且无错误: cards=%d err=%v", len(cards), err)
	}
	if stats, err := cache.GetStats(ctx, []int64{1, 2}); err != nil || len(stats) != 0 {
		t.Fatalf("计数未命中应返回空集且无错误: stats=%d err=%v", len(stats), err)
	}
	if items, err := cache.ListHotWindow(ctx); err != nil || len(items) != 0 {
		t.Fatalf("空窗口应返回空列表且无错误: items=%d err=%v", len(items), err)
	}
}

// TestFeedCacheFaultReturnsError 验证 Redis 不可用时各读接口返回错误，调用方可据此回源且不回填。
func TestFeedCacheFaultReturnsError(t *testing.T) {
	ctx := context.Background()
	cache, server := newFaultTestCache(t)
	server.Close()

	if _, _, err := cache.GetPage(ctx, "digest"); err == nil {
		t.Fatal("Redis 故障时 GetPage 必须返回错误而不是按未命中处理")
	}
	if _, err := cache.FeedVersion(ctx); err == nil {
		t.Fatal("Redis 故障时 FeedVersion 必须返回错误，不能退化为默认版本")
	}
	if _, err := cache.GetCards(ctx, []int64{1}); err == nil {
		t.Fatal("Redis 故障时 GetCards 必须返回错误而不是空集")
	}
	if _, err := cache.GetStats(ctx, []int64{1}); err == nil {
		t.Fatal("Redis 故障时 GetStats 必须返回错误而不是空集")
	}
	if _, err := cache.ListHotWindow(ctx); !errors.Is(err, ErrHotWindowUnavailable) {
		t.Fatalf("热度窗口全桶失败应返回可用性错误: err=%v", err)
	}
}
