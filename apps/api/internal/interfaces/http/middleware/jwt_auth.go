// jwt_auth.go 提供必选 JWT、可选 JWT 与内部 token 三种 Gin 鉴权中间件。
package middleware

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	infrajwt "pulsefeed/internal/infra/jwt"

	"github.com/gin-gonic/gin"
)

// ContextUserIDKey 是鉴权成功后写入 gin.Context 的用户 ID 键名。
const ContextUserIDKey = "user_id"

// ContextJTIKey / ContextFamilyIDKey / ContextExpKey 保存登出所需的三项令牌信息。
const (
	ContextJTIKey      = "auth_jti"
	ContextFamilyIDKey = "auth_family_id"
	ContextExpKey      = "auth_exp"
)

const (
	codeAccessExpired   = 40101
	codeAccessInvalid   = 40102
	denylistErrorMetric = "read_error"
)

// Denylist 是拒绝名单读取端口，Redis 实现未注入时传 nil 即跳过该检查。
type Denylist interface {
	// Blocked 同时检查 jti 与会话链，任一命中即视为已吊销。
	Blocked(ctx context.Context, jti, familyID string) (bool, error)
}

// AuthMetrics 是鉴权链路的指标端口，未注入时为空操作。
type AuthMetrics interface {
	ObserveDenylistError(result string)
}

// AccessInfo 是中间件校验通过后写入上下文的访问令牌信息。
type AccessInfo struct {
	JTI       string
	FamilyID  string
	ExpiresAt time.Time
}

// NewJWTAuth 要求请求携带合法 Bearer 令牌；过期返回 40101，其余失败返回 40102。
func NewJWTAuth(issuer *infrajwt.Issuer, denylist Denylist, metrics AuthMetrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, err := issuer.Parse(bearerToken(c.GetHeader("Authorization")))
		if err != nil {
			if errors.Is(err, infrajwt.ErrTokenExpired) {
				abortUnauthorized(c, codeAccessExpired, "access token expired")
			} else {
				abortUnauthorized(c, codeAccessInvalid, "missing or invalid token")
			}
			return
		}
		if blocked, err := isBlocked(c.Request.Context(), denylist, claims.JTI, claims.FamilyID, metrics); err != nil {
			log.Printf("warning: 查询拒绝名单失败，按放行处理 (path=%s): %v", c.Request.URL.Path, err)
		} else if blocked {
			abortUnauthorized(c, codeAccessInvalid, "access token revoked")
			return
		}
		storeClaims(c, claims)
		c.Next()
	}
}

// NewOptionalJWTAuth 用于「登录可选」的读接口：任何鉴权失败都降级为匿名访问而不是中断请求。
func NewOptionalJWTAuth(issuer *infrajwt.Issuer, denylist Denylist, metrics AuthMetrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			log.Printf("optional auth: 未携带 Authorization，按匿名访问继续 (path=%s)", c.Request.URL.Path)
			c.Next()
			return
		}
		token := bearerToken(header)
		if token == "" {
			log.Printf("optional auth: Authorization 头不是 Bearer 格式或令牌为空，按匿名访问继续 (path=%s)", c.Request.URL.Path)
			c.Next()
			return
		}
		claims, err := issuer.Parse(token)
		if err != nil {
			log.Printf("optional auth: 令牌解析失败，按匿名访问继续 (path=%s): %v", c.Request.URL.Path, err)
			c.Next()
			return
		}
		if blocked, err := isBlocked(c.Request.Context(), denylist, claims.JTI, claims.FamilyID, metrics); err != nil {
			log.Printf("warning: 查询拒绝名单失败，按放行处理 (path=%s): %v", c.Request.URL.Path, err)
		} else if blocked {
			log.Printf("optional auth: 令牌命中拒绝名单，按匿名访问继续 (path=%s)", c.Request.URL.Path)
			c.Next()
			return
		}
		storeClaims(c, claims)
		c.Next()
	}
}

// NewInternalTokenAuth 使用常量时间比较校验 X-Internal-Token 头，失败以 401 终止请求。
func NewInternalTokenAuth(token string) gin.HandlerFunc {
	expected := []byte(token)
	return func(c *gin.Context) {
		provided := []byte(c.GetHeader("X-Internal-Token"))
		if len(provided) == 0 || subtle.ConstantTimeCompare(provided, expected) != 1 {
			abortUnauthorized(c, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		c.Next()
	}
}

// UserIDFromContext 读取必选/可选 JWT 中间件写入的用户 ID。
func UserIDFromContext(c *gin.Context) (int64, bool) {
	value, ok := c.Get(ContextUserIDKey)
	if !ok {
		return 0, false
	}
	userID, ok := value.(int64)
	return userID, ok
}

// AccessInfoFrom 读取中间件写入的 jti、family 与过期时间，供登出使用。
func AccessInfoFrom(c *gin.Context) (AccessInfo, bool) {
	jti, _ := c.Get(ContextJTIKey)
	familyID, _ := c.Get(ContextFamilyIDKey)
	exp, _ := c.Get(ContextExpKey)
	info := AccessInfo{}
	if value, ok := jti.(string); ok {
		info.JTI = value
	}
	if value, ok := familyID.(string); ok {
		info.FamilyID = value
	}
	expiresAt, ok := exp.(time.Time)
	if !ok {
		return info, false
	}
	info.ExpiresAt = expiresAt
	return info, true
}

// storeClaims 将用户 ID、jti、family 与过期时间写入上下文。
func storeClaims(c *gin.Context, claims *infrajwt.Claims) {
	c.Set(ContextUserIDKey, claims.UserID)
	c.Set(ContextJTIKey, claims.JTI)
	c.Set(ContextFamilyIDKey, claims.FamilyID)
	c.Set(ContextExpKey, claims.ExpiresAt)
}

// isBlocked 查询拒绝名单；denylist 为 nil、jti 与 family 均为空时直接放行。
func isBlocked(ctx context.Context, denylist Denylist, jti, familyID string, metrics AuthMetrics) (bool, error) {
	if denylist == nil || (jti == "" && familyID == "") {
		return false, nil
	}
	blocked, err := denylist.Blocked(ctx, jti, familyID)
	if err != nil {
		if metrics != nil {
			metrics.ObserveDenylistError(denylistErrorMetric)
		}
		return false, err
	}
	return blocked, nil
}

// bearerToken 从 Authorization 头提取 Bearer 令牌；格式不符时返回空串。
func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// abortUnauthorized 以统一错误结构与指定业务码终止请求。
func abortUnauthorized(c *gin.Context, code int, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"code":    code,
		"message": message,
	})
}
