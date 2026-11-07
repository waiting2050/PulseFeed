// jwt.go 提供 HS256 访问令牌的签发与校验能力，供认证中间件与账号应用服务使用。
package infrajwt

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken 表示令牌缺失、格式非法、签名错误或已过期。
var ErrInvalidToken = errors.New("invalid token")

// Issuer 负责签发与校验访问令牌，仅支持 HS256 签名。
type Issuer struct {
	secret    []byte
	accessTTL time.Duration
}

// New 创建令牌签发器；secret 为空或 accessTTL 非正时返回错误。
func New(secret string, accessTTL time.Duration) (*Issuer, error) {
	if secret == "" {
		return nil, errors.New("jwt secret 不能为空")
	}
	if accessTTL <= 0 {
		return nil, errors.New("jwt access_ttl 必须为正数")
	}
	return &Issuer{secret: []byte(secret), accessTTL: accessTTL}, nil
}

// AccessTTL 返回令牌有效期，供传输层回显 expires_in。
func (i *Issuer) AccessTTL() time.Duration {
	return i.accessTTL
}

// claims 携带用户角色的自定义声明，Subject 固定为字符串形式的用户 ID。
type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// Issue 为指定用户签发访问令牌，过期时间为签发时刻加 accessTTL。
func (i *Issuer) Issue(userID int64, role string) (string, error) {
	now := time.Now()
	c := claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.accessTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(i.secret)
}

// Parse 校验令牌签名与有效期，返回用户 ID 与角色；任何失败统一返回 ErrInvalidToken。
func (i *Issuer) Parse(tokenString string) (int64, string, error) {
	if tokenString == "" {
		return 0, "", ErrInvalidToken
	}
	c := &claims{}
	_, err := jwt.ParseWithClaims(tokenString, c, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("非预期的签名算法: %v", t.Header["alg"])
		}
		return i.secret, nil
	})
	if err != nil {
		return 0, "", ErrInvalidToken
	}
	userID, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || userID <= 0 {
		return 0, "", ErrInvalidToken
	}
	return userID, c.Role, nil
}
