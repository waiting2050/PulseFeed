// jwt_auth.go 提供必选 JWT、可选 JWT 与内部 token 三种 Gin 鉴权中间件。
package middleware

import (
	"crypto/subtle"
	"log"
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

// NewOptionalJWTAuth 用于「登录可选」的读接口：任何鉴权失败都降级为匿名访问而不是中断请求。
// 未携带 token、token 格式非法、token 解析失败三种情况行为一致（不写入用户 ID 直接放行），
// 仅通过日志区分原因，便于排查客户端凭据问题。
func NewOptionalJWTAuth(issuer *infrajwt.Issuer) gin.HandlerFunc {
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
		userID, _, err := issuer.Parse(token)
		if err != nil {
			log.Printf("optional auth: 令牌解析失败，按匿名访问继续 (path=%s): %v", c.Request.URL.Path, err)
			c.Next()
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
