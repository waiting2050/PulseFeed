package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"pulsefeed/internal/shared/httpx"
	"pulsefeed/internal/shared/metrics"
)

// Counter 是限流所需的最小存储接口，Redis 不可用时 middleware 会放行请求。
type Counter interface {
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

type redisCounter struct {
	client *redis.Client
}

func NewRedisCounter(client *redis.Client) Counter {
	if client == nil {
		return nil
	}
	return &redisCounter{client: client}
}

func (c *redisCounter) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := c.client.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

// RateLimitOptions 描述一次限流配置。
type RateLimitOptions struct {
	Counter Counter
	Limit   int
	Window  time.Duration
	Scope   string
	Logger  *slog.Logger
	Metrics *metrics.Registry
}

// RateLimit 按固定窗口计数做限流，维度优先取登录用户，其次取客户端 IP。
func RateLimit(opts RateLimitOptions) gin.HandlerFunc {
	window := opts.Window
	if window <= 0 {
		window = time.Minute
	}
	if opts.Limit <= 0 || opts.Counter == nil {
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		now := time.Now()
		identity := "ip:" + c.ClientIP()
		if userID, ok := CurrentUserID(c); ok {
			identity = "user:" + strconv.FormatInt(userID, 10)
		}
		bucket := now.Unix() / int64(window.Seconds())
		key := fmt.Sprintf("ratelimit:v1:%s:%s:%d", opts.Scope, identity, bucket)

		count, err := opts.Counter.Incr(c.Request.Context(), key, window)
		if err != nil {
			// 限流依赖不可用时放行请求，避免缓存故障放大为全站不可用。
			if opts.Logger != nil {
				opts.Logger.Warn("rate limiter unavailable, request allowed",
					"request_id", RequestIDFrom(c),
					"error", err,
				)
			}
			c.Next()
			return
		}

		if count > int64(opts.Limit) {
			if opts.Metrics != nil {
				opts.Metrics.ObserveRateLimited(c.FullPath())
			}
			retryAfter := window - time.Duration(now.UnixNano()%int64(window))
			c.Header("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			httpx.TooManyRequests(c, "请求过于频繁，请稍后再试")
			c.Abort()
			return
		}

		c.Next()
	}
}
