// interaction_guard_test.go 验证互动写限流：窗口内放行、超限拒绝并带剩余时长、Redis 故障 fail-open。
package cache

import (
	"context"
	"testing"
)

// TestInteractionGuardAllowThenReject 验证固定窗口计数：达到阈值后拒绝，并返回窗口剩余 TTL。
func TestInteractionGuardAllowThenReject(t *testing.T) {
	cache, _ := newFaultTestCache(t)
	guard := NewInteractionGuard(cache.client)
	ctx := context.Background()

	for i := 0; i < interactionUserLimit; i++ {
		allowed, _, err := guard.AllowUser(ctx, 7)
		if err != nil || !allowed {
			t.Fatalf("第 %d 次请求应放行: allowed=%v err=%v", i+1, allowed, err)
		}
	}
	allowed, retryAfter, err := guard.AllowUser(ctx, 7)
	if err != nil {
		t.Fatalf("超限不应返回错误: %v", err)
	}
	if allowed {
		t.Fatal("超过窗口阈值应拒绝")
	}
	if retryAfter <= 0 {
		t.Fatalf("拒绝时应返回正的建议等待时长，实际 %v", retryAfter)
	}
}

// TestInteractionGuardFailOpen 验证 Redis 不可用时放行并返回错误，由调用方 fail-open 并计指标。
func TestInteractionGuardFailOpen(t *testing.T) {
	cache, server := newFaultTestCache(t)
	guard := NewInteractionGuard(cache.client)
	server.Close()

	allowed, _, err := guard.AllowUser(context.Background(), 7)
	if !allowed || err == nil {
		t.Fatalf("Redis 故障应 fail-open 并返回错误: allowed=%v err=%v", allowed, err)
	}

	// 未配置 Redis 时直接放行。
	if allowed, _, err := NewInteractionGuard(nil).AllowUser(context.Background(), 7); !allowed || err != nil {
		t.Fatalf("未配置 Redis 应直接放行: allowed=%v err=%v", allowed, err)
	}
}
