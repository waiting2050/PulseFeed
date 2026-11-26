// session.go 定义刷新令牌会话链的领域模型与仓储/缓存/限流端口，应用层只依赖这些接口。
package domainaccount

import (
	"context"
	"time"
)

// RefreshToken 是一枚刷新令牌的持久化记录；明文不落库，只保存 SHA-256 十六进制哈希。
type RefreshToken struct {
	ID            int64
	UserID        int64
	FamilyID      string
	TokenHash     string
	RotatedFrom   int64
	DeviceLabel   string
	IssuedAt      time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
}

// RotateOutcome 描述一次轮换尝试的结果分类。
type RotateOutcome int

const (
	// RotateOK 旧行有效，已标记轮换并插入新行。
	RotateOK RotateOutcome = iota
	// RotateNotFound 哈希不存在。
	RotateNotFound
	// RotateExpired 已过期，未触发其他链吊销。
	RotateExpired
	// RotateGrace 已轮换且仍在宽限期内，需要回放缓存。
	RotateGrace
	// RotateReplay 已吊销且超出宽限期，判定为重放。
	RotateReplay
)

// RotateResult 是轮换结果，携带定位会话链与用户所需的信息。
type RotateResult struct {
	Outcome   RotateOutcome
	UserID    int64
	FamilyID  string
	RevokedAt time.Time
}

// SessionRepository 描述刷新令牌的持久化能力。
type SessionRepository interface {
	// Create 登录时开一条新链。
	Create(ctx context.Context, token *RefreshToken) error
	// FindByHash 按哈希读取一行（无论是否已吊销），仅用于限流前解析用户。
	FindByHash(ctx context.Context, hash string) (*RefreshToken, error)
	// Rotate 在同一事务内锁定旧行、判定状态、标记轮换并插入新行。
	Rotate(ctx context.Context, oldHash string, next *RefreshToken, grace time.Duration) (*RotateResult, error)
	// FindActiveByHash 读取指定哈希的当前状态，用于宽限期回放前的二次校验。
	FindActiveByHash(ctx context.Context, hash string) (*RefreshToken, error)
	// RevokeFamily 吊销单条链，返回受影响行数。
	RevokeFamily(ctx context.Context, userID int64, familyID, reason string) (int64, error)
	// RevokeAll 吊销该用户全部未失效的行，返回受影响行数。
	RevokeAll(ctx context.Context, userID int64, reason string) (int64, error)
}

// RotationRecord 是宽限期缓存中保存的令牌对，必须在窗口内原样回放。
type RotationRecord struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	UserID           int64  `json:"user_id"`
	Account          string `json:"account"`
	FamilyID         string `json:"family_id"`
	IssuedAtUnix     int64  `json:"issued_at_unix"`
}

// RotationCache 按旧 token 哈希缓存轮换结果，供并发重复提交回放。
type RotationCache interface {
	// Save 写入缓存；实现方在 Redis 未配置时应静默跳过。
	Save(ctx context.Context, oldHash string, record RotationRecord, ttl time.Duration) error
	// Load 读取缓存，未命中返回 (nil, false, nil)。
	Load(ctx context.Context, oldHash string) (*RotationRecord, bool, error)
}

// RefreshLimiter 是刷新接口的三层限流端口；Redis 不可用时实现方应 fail-open。
type RefreshLimiter interface {
	AllowToken(ctx context.Context, tokenHash string) (bool, error)
	AllowIP(ctx context.Context, clientIP string) (bool, error)
	AllowUser(ctx context.Context, userID int64) (bool, error)
}

// LoginLimiter 是登录接口的双层限流端口：IP 维度防撞库，账号维度防单账号爆破。
// Redis 不可用时实现方应 fail-open，由调用方记录告警与指标。
type LoginLimiter interface {
	AllowIP(ctx context.Context, clientIP string) (bool, error)
	AllowAccount(ctx context.Context, account string) (bool, error)
}

// AccessDenylist 是登出后拒绝名单的读取与写入端口。
type AccessDenylist interface {
	// Blocked 判断 jti 或会话链是否已被拒绝；任一命中即视为已吊销，Redis 不可用时返回 (false, nil) 实现 fail-open。
	Blocked(ctx context.Context, jti, familyID string) (bool, error)
	// AddToken 将单枚 access token 写入拒绝名单；TTL 必须覆盖其剩余有效期与中间件容差。
	AddToken(ctx context.Context, jti string, ttl time.Duration) error
	// AddFamily 将整条会话链写入拒绝名单，使该链此前轮换出的全部 access token 同时失效。
	AddFamily(ctx context.Context, familyID string, ttl time.Duration) error
}
