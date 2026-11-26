// feed_guard.go 实现 following/hot 的请求级限流：按读者或来源 IP 做 Redis 固定窗口计数，
// Redis 不可用时 fail-open 放行（与登录限流一致），避免限流本身成为可用性单点。
package cache

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	feedUserKeyPrefix = "pf:v1:feed:rl_user:"
	feedIPKeyPrefix   = "pf:v1:feed:rl_ip:"

	feedWindow = 60 * time.Second

	// feedUserLimit 是单个读者每分钟的 following/hot 读取上限：正常浏览（含快速翻页）
	// 远低于该值，超限通常意味着客户端重试风暴或脚本抓取。
	feedUserLimit = 60
	// feedIPLimit 是单个来源 IP 每分钟的匿名读取上限：高于读者阈值，
	// 容忍办公网/NAT 后的正常共用，同时压制单 IP 的批量抓取。
	feedIPLimit = 120
)

// FeedGuard 基于 Redis 固定窗口计数实现 Feed 请求级限流；client 为 nil 时全部放行。
type FeedGuard struct {
	client *redis.Client
}

// NewFeedGuard 创建 Feed 限流器。
func NewFeedGuard(client *redis.Client) *FeedGuard {
	return &FeedGuard{client: client}
}

// AllowUser 限制单个读者的 following/hot 读取频率；超限时返回窗口剩余时长。
func (g *FeedGuard) AllowUser(ctx context.Context, viewerID int64) (bool, time.Duration, error) {
	return g.allow(ctx, feedUserKeyPrefix+strconv.FormatInt(viewerID, 10), feedUserLimit)
}

// AllowIP 限制单个来源 IP 的匿名读取频率；超限时返回窗口剩余时长。
func (g *FeedGuard) AllowIP(ctx context.Context, clientIP string) (bool, time.Duration, error) {
	return g.allow(ctx, feedIPKeyPrefix+clientIP, feedIPLimit)
}

// allow 执行一次 INCR + 首次 EXPIRE 的固定窗口计数。
// 返回 (allowed, retryAfter, err)：err 非 nil 表示 Redis 故障，调用方必须 fail-open；
// 拒绝时 retryAfter 取键的剩余 TTL，用于 Retry-After 响应头。
func (g *FeedGuard) allow(ctx context.Context, key string, limit int) (bool, time.Duration, error) {
	if g == nil || g.client == nil {
		return true, 0, nil
	}
	count, err := g.client.Incr(ctx, key).Result()
	if err != nil {
		return true, 0, err
	}
	if count == 1 {
		if err := g.client.Expire(ctx, key, feedWindow).Err(); err != nil {
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
