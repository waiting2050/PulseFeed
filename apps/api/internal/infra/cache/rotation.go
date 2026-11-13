// rotation.go 缓存轮换结果：同一旧刷新令牌在宽限窗口内重复提交时回放同一对令牌。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"github.com/redis/go-redis/v9"
)

// rotationKeyPrefix 是轮换结果缓存的键前缀，后接旧令牌的 SHA-256 十六进制。
const rotationKeyPrefix = "pf:v1:auth:rotate:"

// RotationCache 基于 Redis 保存轮换结果；client 为 nil 时所有操作静默跳过。
type RotationCache struct {
	client *redis.Client
}

// 编译期断言：确保实现满足领域端口。
var _ domainaccount.RotationCache = (*RotationCache)(nil)

// NewRotationCache 创建轮换结果缓存。
func NewRotationCache(client *redis.Client) *RotationCache {
	return &RotationCache{client: client}
}

// Enabled 返回缓存是否可用（Redis 已配置）。
func (c *RotationCache) Enabled() bool {
	return c != nil && c.client != nil
}

// Save 写入轮换结果，TTL 与宽限窗口一致；Redis 未配置时直接返回成功。
func (c *RotationCache) Save(ctx context.Context, oldHash string, record domainaccount.RotationRecord, ttl time.Duration) error {
	if !c.Enabled() {
		return nil
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, rotationKeyPrefix+oldHash, payload, ttl).Err()
}

// Load 读取轮换结果；未命中返回 (nil, false, nil)，Redis 未配置按未命中处理。
func (c *RotationCache) Load(ctx context.Context, oldHash string) (*domainaccount.RotationRecord, bool, error) {
	if !c.Enabled() {
		return nil, false, nil
	}
	payload, err := c.client.Get(ctx, rotationKeyPrefix+oldHash).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var record domainaccount.RotationRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return nil, false, err
	}
	return &record, true, nil
}
