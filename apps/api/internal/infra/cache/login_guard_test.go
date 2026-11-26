// login_guard_test.go 用进程内 miniredis 验证登录接口的双层限流：
// 同 IP、同账号分别按固定窗口计数；账号名以哈希进入键名；Redis 故障时 fail-open。
package cache

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newLoginGuardTestClient 启动进程内 Redis 并返回登录限流器。
func newLoginGuardTestClient(t *testing.T) (*LoginGuard, *miniredis.Miniredis) {
	t.Helper()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return NewLoginGuard(client), server
}

// TestLoginGuardIPLimit 验证单 IP 在窗口内达到阈值后拒绝，且计数键带 TTL。
func TestLoginGuardIPLimit(t *testing.T) {
	guard, server := newLoginGuardTestClient(t)
	ctx := context.Background()

	for i := 1; i <= loginIPLimit; i++ {
		allowed, err := guard.AllowIP(ctx, "203.0.113.9")
		if err != nil || !allowed {
			t.Fatalf("第 %d 次应放行，allowed=%v err=%v", i, allowed, err)
		}
	}
	allowed, err := guard.AllowIP(ctx, "203.0.113.9")
	if err != nil {
		t.Fatalf("超限不应返回错误: %v", err)
	}
	if allowed {
		t.Fatalf("超过单 IP 阈值应拒绝")
	}
	if ttl := server.TTL(loginIPKeyPrefix + "203.0.113.9"); ttl <= 0 {
		t.Fatalf("限流键 TTL 应为正，实际 %v", ttl)
	}
	// 其他 IP 不受影响。
	if allowed, err = guard.AllowIP(ctx, "198.51.100.7"); err != nil || !allowed {
		t.Fatalf("其他 IP 应放行，allowed=%v err=%v", allowed, err)
	}
}

// TestLoginGuardAccountLimit 验证同账号达到阈值后拒绝，且规范化后大小写共享同一计数。
func TestLoginGuardAccountLimit(t *testing.T) {
	guard, _ := newLoginGuardTestClient(t)
	ctx := context.Background()

	for i := 1; i <= loginAccountLimit; i++ {
		allowed, err := guard.AllowAccount(ctx, "  Alice  ")
		if err != nil || !allowed {
			t.Fatalf("第 %d 次应放行，allowed=%v err=%v", i, allowed, err)
		}
	}
	allowed, err := guard.AllowAccount(ctx, "ALICE")
	if err != nil {
		t.Fatalf("超限不应返回错误: %v", err)
	}
	if allowed {
		t.Fatalf("规范化后的同账号应共享计数并被拒绝")
	}
	// 其他账号不受影响。
	if allowed, err = guard.AllowAccount(ctx, "bob"); err != nil || !allowed {
		t.Fatalf("其他账号应放行，allowed=%v err=%v", allowed, err)
	}
}

// TestLoginGuardAccountKeyHashed 验证账号名不以明文进入 Redis 键名。
func TestLoginGuardAccountKeyHashed(t *testing.T) {
	guard, server := newLoginGuardTestClient(t)
	ctx := context.Background()

	if allowed, err := guard.AllowAccount(ctx, "alice"); err != nil || !allowed {
		t.Fatalf("首次应放行，allowed=%v err=%v", allowed, err)
	}
	keys := server.Keys()
	if len(keys) != 1 {
		t.Fatalf("应只有一个计数键，实际 %v", keys)
	}
	if !strings.HasPrefix(keys[0], loginAccountKeyPrefix) {
		t.Fatalf("键前缀不符: %s", keys[0])
	}
	if strings.Contains(strings.ToLower(keys[0]), "alice") {
		t.Fatalf("账号名不应以明文进入键名: %s", keys[0])
	}
}

// TestLoginGuardFailsOpenOnRedisError 验证 Redis 不可用时两层限流都放行并返回错误供调用方告警。
func TestLoginGuardFailsOpenOnRedisError(t *testing.T) {
	guard, server := newLoginGuardTestClient(t)
	ctx := context.Background()
	server.Close()

	allowed, err := guard.AllowIP(ctx, "203.0.113.9")
	if !allowed || err == nil {
		t.Fatalf("IP 维度应 fail-open 并返回错误，allowed=%v err=%v", allowed, err)
	}
	if allowed, err = guard.AllowAccount(ctx, "alice"); !allowed || err == nil {
		t.Fatalf("账号维度应 fail-open 并返回错误，allowed=%v err=%v", allowed, err)
	}
}
