// refresh_guard.go 实现刷新接口的三层限流：单 token、单 IP、单用户；Redis 不可用时 fail-open。
package cache

import (
	"context"
	"fmt"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"github.com/redis/go-redis/v9"
)

const (
	refreshTokenKeyPrefix = "pf:v1:auth:refresh_tok:"
	refreshIPKeyPrefix    = "pf:v1:auth:refresh_ip:"
	refreshUserKeyPrefix  = "pf:v1:auth:refresh_user:"

	refreshWindow = 60 * time.Second

	refreshTokenLimit = 20
	refreshIPLimit    = 60
	refreshUserLimit  = 30
)

// RefreshGuard 基于 Redis 计数实现三层刷新限流；client 为 nil 时全部放行。
type RefreshGuard struct {
	client *redis.Client
}

// 编译期断言：确保实现满足领域端口。
var _ domainaccount.RefreshLimiter = (*RefreshGuard)(nil)

// NewRefreshGuard 创建刷新限流器。
func NewRefreshGuard(client *redis.Client) *RefreshGuard {
	return &RefreshGuard{client: client}
}

// AllowToken 限制单枚旧 token 的提交频率。
func (g *RefreshGuard) AllowToken(ctx context.Context, tokenHash string) (bool, error) {
	// 用哈希前 16 位作为键，既足够区分也不需要把完整哈希写进 Redis 键名。
	if len(tokenHash) > 16 {
		tokenHash = tokenHash[:16]
	}
	return g.allow(ctx, refreshTokenKeyPrefix+tokenHash, refreshTokenLimit)
}

// AllowIP 限制单来源 IP 的提交频率。
func (g *RefreshGuard) AllowIP(ctx context.Context, clientIP string) (bool, error) {
	return g.allow(ctx, refreshIPKeyPrefix+clientIP, refreshIPLimit)
}

// AllowUser 限制单个用户的提交频率。
func (g *RefreshGuard) AllowUser(ctx context.Context, userID int64) (bool, error) {
	return g.allow(ctx, fmt.Sprintf("%s%d", refreshUserKeyPrefix, userID), refreshUserLimit)
}

// allow 执行一次 INCR + 首次 EXPIRE 的固定窗口计数；Redis 报错或未配置时放行。
func (g *RefreshGuard) allow(ctx context.Context, key string, limit int) (bool, error) {
	if g == nil || g.client == nil {
		return true, nil
	}
	count, err := g.client.Incr(ctx, key).Result()
	if err != nil {
		return true, err
	}
	if count == 1 {
		if err := g.client.Expire(ctx, key, refreshWindow).Err(); err != nil {
			return true, err
		}
	}
	if count > int64(limit) {
		return false, nil
	}
	return true, nil
}
