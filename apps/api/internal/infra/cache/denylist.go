// denylist.go 维护登出后的 access token 拒绝名单；Redis 不可用时读取按放行处理（fail-open）。
package cache

import (
	"context"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"github.com/redis/go-redis/v9"
)

const (
	// denylistKeyPrefix 是 jti 拒绝名单的键前缀。
	denylistKeyPrefix = "pf:v1:auth:denylist:"
	// denylistFamilyKeyPrefix 是会话链拒绝名单的键前缀：登出后该链的全部 access token 一并失效。
	denylistFamilyKeyPrefix = "pf:v1:auth:denylist_family:"
	// minDenylistTTL 是拒绝名单的最小存活时间，保证 ttl<=0（宽限期内已过期令牌登出）
	// 仍能覆盖中间件容差窗口，不会写入即过期。
	minDenylistTTL = 30 * time.Second
)

// Denylist 基于 Redis 保存已登出 access token 的 jti 与会话链；client 为 nil 时读放行、写跳过。
type Denylist struct {
	client *redis.Client
}

// 编译期断言：确保实现满足领域端口。
var _ domainaccount.AccessDenylist = (*Denylist)(nil)

// NewDenylist 创建拒绝名单。
func NewDenylist(client *redis.Client) *Denylist {
	return &Denylist{client: client}
}

// Blocked 同时检查 jti 与会话链是否在拒绝名单中；Redis 未配置或报错时返回 false 放行。
func (d *Denylist) Blocked(ctx context.Context, jti, familyID string) (bool, error) {
	if d == nil || d.client == nil {
		return false, nil
	}
	keys := make([]string, 0, 2)
	if jti != "" {
		keys = append(keys, denylistKeyPrefix+jti)
	}
	if familyID != "" {
		keys = append(keys, denylistFamilyKeyPrefix+familyID)
	}
	if len(keys) == 0 {
		return false, nil
	}
	exists, err := d.client.Exists(ctx, keys...).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

// AddToken 将 jti 写入拒绝名单；TTL 不足最小值时按最小值写入，避免键先于中间件容差过期。
func (d *Denylist) AddToken(ctx context.Context, jti string, ttl time.Duration) error {
	if d == nil || d.client == nil || jti == "" {
		return nil
	}
	return d.client.Set(ctx, denylistKeyPrefix+jti, "1", clampDenylistTTL(ttl)).Err()
}

// AddFamily 将整条会话链写入拒绝名单；TTL 由调用方按 access 有效期加容差计算。
func (d *Denylist) AddFamily(ctx context.Context, familyID string, ttl time.Duration) error {
	if d == nil || d.client == nil || familyID == "" {
		return nil
	}
	return d.client.Set(ctx, denylistFamilyKeyPrefix+familyID, "1", clampDenylistTTL(ttl)).Err()
}

// clampDenylistTTL 把非正或过短的 TTL 提升到最小存活时间。
func clampDenylistTTL(ttl time.Duration) time.Duration {
	if ttl < minDenylistTTL {
		return minDenylistTTL
	}
	return ttl
}
