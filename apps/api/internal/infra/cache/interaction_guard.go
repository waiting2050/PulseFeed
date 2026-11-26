// interaction_guard.go 实现点赞/收藏/评论写接口的用户级限流：
// 按用户做 Redis 固定窗口计数，Redis 不可用时 fail-open 放行（与登录/feed 限流一致），
// 避免限流器本身成为互动能力的可用性单点。
package cache

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	interactionUserKeyPrefix = "pf:v1:interaction:rl_user:"

	interactionWindow = 60 * time.Second

	// interactionUserLimit 是单个用户每分钟的互动写上限（点赞、收藏、评论合计）。
	// 依据：feed 读取的用户阈值是 60 次/分钟，互动写是读取动作的子集，
	// 但用户连续划 feed 时可能快速双击点赞/收藏，取 120 次/分钟给突发与客户端重试留一倍余量，
	// 同时把脚本刷量压到平均 2 次/秒以内。
	interactionUserLimit = 120
)

// InteractionGuard 基于 Redis 固定窗口计数实现互动写限流；client 为 nil 时全部放行。
type InteractionGuard struct {
	client *redis.Client
}

// NewInteractionGuard 创建互动写限流器。
func NewInteractionGuard(client *redis.Client) *InteractionGuard {
	return &InteractionGuard{client: client}
}

// AllowUser 限制单个用户的互动写频率；超限时返回窗口剩余时长。
func (g *InteractionGuard) AllowUser(ctx context.Context, userID int64) (bool, time.Duration, error) {
	return g.allow(ctx, interactionUserKeyPrefix+strconv.FormatInt(userID, 10), interactionUserLimit)
}

// allow 执行一次 INCR + 首次 EXPIRE 的固定窗口计数。
// 返回 (allowed, retryAfter, err)：err 非 nil 表示 Redis 故障，调用方必须 fail-open；
// 拒绝时 retryAfter 取键的剩余 TTL，用于 Retry-After 响应头。
func (g *InteractionGuard) allow(ctx context.Context, key string, limit int) (bool, time.Duration, error) {
	if g == nil || g.client == nil {
		return true, 0, nil
	}
	count, err := g.client.Incr(ctx, key).Result()
	if err != nil {
		return true, 0, err
	}
	if count == 1 {
		if err := g.client.Expire(ctx, key, interactionWindow).Err(); err != nil {
			return true, 0, err
		}
	}
	if count > int64(limit) {
		retryAfter, err := g.client.TTL(ctx, key).Result()
		if err != nil || retryAfter <= 0 {
			// TTL 读取失败或键已过期时给一个保守的等待时长，避免 Retry-After: 0。
			retryAfter = time.Second
		}
		return false, retryAfter, nil
	}
	return true, 0, nil
}
