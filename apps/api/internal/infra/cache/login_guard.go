// login_guard.go 实现登录接口的双层限流：单 IP 防撞库、单账号防爆破；Redis 不可用时 fail-open。
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"github.com/redis/go-redis/v9"
)

const (
	loginIPKeyPrefix      = "pf:v1:auth:login_ip:"
	loginAccountKeyPrefix = "pf:v1:auth:login_acct:"

	loginWindow = 60 * time.Second

	// loginIPLimit 略高于刷新接口的用户阈值，允许办公网/NAT 后的正常登录，同时压制撞库。
	loginIPLimit = 30
	// loginAccountLimit 限制单账号每分钟的失败尝试，压制定向爆破。
	loginAccountLimit = 10
)

// LoginGuard 基于 Redis 固定窗口计数实现登录限流；client 为 nil 时全部放行。
type LoginGuard struct {
	client *redis.Client
}

// 编译期断言：确保实现满足领域端口。
var _ domainaccount.LoginLimiter = (*LoginGuard)(nil)

// NewLoginGuard 创建登录限流器。
func NewLoginGuard(client *redis.Client) *LoginGuard {
	return &LoginGuard{client: client}
}

// AllowIP 限制单来源 IP 的登录尝试频率。
func (g *LoginGuard) AllowIP(ctx context.Context, clientIP string) (bool, error) {
	return g.allow(ctx, loginIPKeyPrefix+clientIP, loginIPLimit)
}

// AllowAccount 限制单个账号的登录尝试频率；账号名只以哈希前 16 位进入键名，避免明文落 Redis。
func (g *LoginGuard) AllowAccount(ctx context.Context, account string) (bool, error) {
	sum := sha256.Sum256([]byte(domainaccount.NormalizeAccountName(account)))
	suffix := hex.EncodeToString(sum[:])[:16]
	return g.allow(ctx, loginAccountKeyPrefix+suffix, loginAccountLimit)
}

// allow 执行一次 INCR + 首次 EXPIRE 的固定窗口计数；Redis 报错或未配置时放行。
func (g *LoginGuard) allow(ctx context.Context, key string, limit int) (bool, error) {
	if g == nil || g.client == nil {
		return true, nil
	}
	count, err := g.client.Incr(ctx, key).Result()
	if err != nil {
		return true, err
	}
	if count == 1 {
		if err := g.client.Expire(ctx, key, loginWindow).Err(); err != nil {
			return true, err
		}
	}
	if count > int64(limit) {
		return false, nil
	}
	return true, nil
}
