// denylist.go 维护登出后的 access token 拒绝名单；Redis 不可用时读取按放行处理（fail-open）。
package cache

import (
	"context"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"github.com/redis/go-redis/v9"
)

// denylistKeyPrefix 是 jti 拒绝名单的键前缀。
const denylistKeyPrefix = "pf:v1:auth:denylist:"

// Denylist 基于 Redis 保存已登出 access token 的 jti；client 为 nil 时读放行、写跳过。
type Denylist struct {
	client *redis.Client
}

// 编译期断言：确保实现满足领域端口。
var _ domainaccount.AccessDenylist = (*Denylist)(nil)

// NewDenylist 创建拒绝名单。
func NewDenylist(client *redis.Client) *Denylist {
	return &Denylist{client: client}
}

// Blocked 判断 jti 是否在拒绝名单中；Redis 未配置或报错时返回 false 放行。
func (d *Denylist) Blocked(ctx context.Context, jti string) (bool, error) {
	if d == nil || d.client == nil || jti == "" {
		return false, nil
	}
	exists, err := d.client.Exists(ctx, denylistKeyPrefix+jti).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

// Add 将 jti 写入拒绝名单，TTL 为 access token 的剩余有效期；已过期则跳过。
func (d *Denylist) Add(ctx context.Context, jti string, ttl time.Duration) error {
	if d == nil || d.client == nil || jti == "" || ttl <= 0 {
		return nil
	}
	return d.client.Set(ctx, denylistKeyPrefix+jti, "1", ttl).Err()
}
