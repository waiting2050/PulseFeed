// redis.go 构造 go-redis/v9 客户端；空 addr 视为禁用缓存并返回 nil（降级开关）。
package cache

import (
	"pulsefeed/internal/infra/config"

	"github.com/redis/go-redis/v9"
)

// New 按配置创建 Redis 客户端；cfg.Addr 为空时返回 nil，调用方按禁用缓存处理。
// 返回 nil 不视为错误：缓存是可选加速层，缺失时业务链路走 DB 降级。
func New(cfg config.RedisConfig) *redis.Client {
	if cfg.Addr == "" {
		return nil
	}
	return redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
}
