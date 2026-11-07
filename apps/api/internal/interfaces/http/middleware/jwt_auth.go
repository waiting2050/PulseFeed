// jwt_auth.go 提供必选 JWT、可选 JWT 与内部 token 三种 Gin 鉴权中间件。
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	infrajwt "pulsefeed/internal/infra/jwt"

	"github.com/gin-gonic/gin"
)

// ContextUserIDKey 是鉴权成功后写入 gin.Context 的用户 ID 键名。
const ContextUserIDKey = "user_id"

// NewJWTAuth 要求请求携带合法 Bearer 令牌，缺失或非法时以 401 终止请求。
func NewJWTAuth(issuer *infrajwt.Issuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, _, err := issuer.Parse(bearerToken(c.GetHeader("Authorization")))
		if err != nil {
			abortUnauthorized(c)
			return
		}
		c.Set(ContextUserIDKey, userID)
		c.Next()
	}
}

// NewOptionalJWTAuth 在未携带令牌时直接放行；携带令牌时必须合法，成功后写入用户 ID。
func NewOptionalJWTAuth(issuer *infrajwt.Issuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.Next()
			return
		}
		userID, _, err := issuer.Parse(bearerToken(header))
		if err != nil {
			abortUnauthorized(c)
			return
		}
		c.Set(ContextUserIDKey, userID)
		c.Next()
	}
}

// NewInternalTokenAuth 使用常量时间比较校验 X-Internal-Token 头，失败以 401 终止请求。
func NewInternalTokenAuth(token string) gin.HandlerFunc {
	expected := []byte(token)
	return func(c *gin.Context) {
		provided := []byte(c.GetHeader("X-Internal-Token"))
		if len(provided) == 0 || subtle.ConstantTimeCompare(provided, expected) != 1 {
			abortUnauthorized(c)
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

// bearerToken 从 Authorization 头提取 Bearer 令牌；格式不符时返回空串。
func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// abortUnauthorized 以统一错误结构终止请求。
func abortUnauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"code":    http.StatusUnauthorized,
		"message": "missing or invalid token",
	})
}
