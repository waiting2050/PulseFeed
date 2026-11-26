// denylist_test.go 用进程内 miniredis 验证拒绝名单的 jti/family 双键语义与 TTL 钳制：
// 任一键命中即拒绝；ttl<=0 时仍写入并提升到最小存活时间，避免容差内令牌复活。
package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newDenylistTestClient 启动进程内 Redis 并返回拒绝名单实例。
func newDenylistTestClient(t *testing.T) (*Denylist, *miniredis.Miniredis) {
	t.Helper()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return NewDenylist(client), server
}

// TestDenylistBlocksTokenAndFamily 验证 jti 与会话链任一命中都会拒绝。
func TestDenylistBlocksTokenAndFamily(t *testing.T) {
	denylist, _ := newDenylistTestClient(t)
	ctx := context.Background()

	blocked, err := denylist.Blocked(ctx, "jti-1", "family-1")
	if err != nil || blocked {
		t.Fatalf("初始状态应放行，blocked=%v err=%v", blocked, err)
	}
	if err := denylist.AddToken(ctx, "jti-1", time.Minute); err != nil {
		t.Fatalf("写入 jti 失败: %v", err)
	}
	if blocked, err = denylist.Blocked(ctx, "jti-1", ""); err != nil || !blocked {
		t.Fatalf("jti 应命中拒绝名单，blocked=%v err=%v", blocked, err)
	}
	if blocked, err = denylist.Blocked(ctx, "jti-2", ""); err != nil || blocked {
		t.Fatalf("其他 jti 不应误伤，blocked=%v err=%v", blocked, err)
	}

	if err := denylist.AddFamily(ctx, "family-1", time.Minute); err != nil {
		t.Fatalf("写入 family 失败: %v", err)
	}
	if blocked, err = denylist.Blocked(ctx, "jti-other", "family-1"); err != nil || !blocked {
		t.Fatalf("family 应命中拒绝名单，blocked=%v err=%v", blocked, err)
	}
	if blocked, err = denylist.Blocked(ctx, "", "family-2"); err != nil || blocked {
		t.Fatalf("其他 family 不应误伤，blocked=%v err=%v", blocked, err)
	}
	if blocked, err = denylist.Blocked(ctx, "", ""); err != nil || blocked {
		t.Fatalf("空标识应直接放行，blocked=%v err=%v", blocked, err)
	}
}

// TestDenylistClampsNonPositiveTTL 验证 ttl<=0 不会被跳过，而是按最小值写入并覆盖容差窗口。
func TestDenylistClampsNonPositiveTTL(t *testing.T) {
	denylist, server := newDenylistTestClient(t)
	ctx := context.Background()

	if err := denylist.AddToken(ctx, "jti-expired", -time.Minute); err != nil {
		t.Fatalf("写入过期 jti 失败: %v", err)
	}
	if ttl := server.TTL(denylistKeyPrefix + "jti-expired"); ttl < minDenylistTTL {
		t.Fatalf("jti 键 TTL 应被钳制到 %v，实际 %v", minDenylistTTL, ttl)
	}

	if err := denylist.AddFamily(ctx, "family-expired", 0); err != nil {
		t.Fatalf("写入 family 失败: %v", err)
	}
	if ttl := server.TTL(denylistFamilyKeyPrefix + "family-expired"); ttl < minDenylistTTL {
		t.Fatalf("family 键 TTL 应被钳制到 %v，实际 %v", minDenylistTTL, ttl)
	}
}

// TestDenylistWithoutClient 验证未配置 Redis 时读放行、写静默跳过。
func TestDenylistWithoutClient(t *testing.T) {
	denylist := NewDenylist(nil)
	ctx := context.Background()

	if blocked, err := denylist.Blocked(ctx, "jti-1", "family-1"); err != nil || blocked {
		t.Fatalf("无 Redis 时应 fail-open，blocked=%v err=%v", blocked, err)
	}
	if err := denylist.AddToken(ctx, "jti-1", time.Minute); err != nil {
		t.Fatalf("无 Redis 时写入应跳过: %v", err)
	}
	if err := denylist.AddFamily(ctx, "family-1", time.Minute); err != nil {
		t.Fatalf("无 Redis 时写入应跳过: %v", err)
	}
}
