// jwt.go 提供 HS256 访问令牌的签发与校验能力，供认证中间件与账号应用服务使用。
package infrajwt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken 表示令牌缺失、格式非法、签名错误、算法不符或缺少必要声明。
var ErrInvalidToken = errors.New("invalid token")

// ErrTokenExpired 表示令牌签名合法但已过期，中间件据此返回 40101。
var ErrTokenExpired = errors.New("token expired")

// Issuer 负责签发与校验访问令牌，仅支持 HS256 签名。
type Issuer struct {
	secret    []byte
	accessTTL time.Duration
	leeway    time.Duration
}

// New 创建令牌签发器；secret 为空、accessTTL 非正或 leeway 为负时返回错误。
func New(secret string, accessTTL, leeway time.Duration) (*Issuer, error) {
	if secret == "" {
		return nil, errors.New("jwt secret 不能为空")
	}
	if accessTTL <= 0 {
		return nil, errors.New("jwt access_ttl 必须为正数")
	}
	if leeway < 0 {
		return nil, errors.New("jwt leeway 不能为负数")
	}
	return &Issuer{secret: []byte(secret), accessTTL: accessTTL, leeway: leeway}, nil
}

// AccessTTL 返回令牌有效期，供传输层回显 expires_in。
func (i *Issuer) AccessTTL() time.Duration {
	return i.accessTTL
}

// Claims 是访问令牌解析后的用户与会话信息。
type Claims struct {
	UserID    int64
	Role      string
	JTI       string
	FamilyID  string
	ExpiresAt time.Time
}

// claims 是 JWT 载荷的自定义声明，jti 由 RegisteredClaims.ID 承载。
type claims struct {
	Role     string `json:"role"`
	FamilyID string `json:"fid,omitempty"`
	jwt.RegisteredClaims
}

// Issue 为指定用户与其会话链签发访问令牌；familyID 为空表示兼容旧调用（不带 fid）。
func (i *Issuer) Issue(userID int64, role, familyID string) (string, error) {
	now := time.Now()
	jti, err := newRandomID(16)
	if err != nil {
		return "", fmt.Errorf("生成 jti 失败: %w", err)
	}
	c := claims{
		Role:     role,
		FamilyID: familyID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.accessTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(i.secret)
}

// Parse 按“签名 → 过期 → 必要声明”校验令牌；过期返回 ErrTokenExpired，其余失败返回 ErrInvalidToken。
func (i *Issuer) Parse(tokenString string) (*Claims, error) {
	if tokenString == "" {
		return nil, ErrInvalidToken
	}
	c := &claims{}
	_, err := jwt.ParseWithClaims(tokenString, c, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("非预期的签名算法: %v", t.Header["alg"])
		}
		return i.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithLeeway(i.leeway),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}
	userID, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || userID <= 0 {
		return nil, ErrInvalidToken
	}
	if c.ExpiresAt == nil || c.ID == "" {
		return nil, ErrInvalidToken
	}
	return &Claims{
		UserID:    userID,
		Role:      c.Role,
		JTI:       c.ID,
		FamilyID:  c.FamilyID,
		ExpiresAt: c.ExpiresAt.Time,
	}, nil
}

// newRandomID 生成随机数的 base64url 文本，用于 jti 与实例级随机标识。
func newRandomID(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
