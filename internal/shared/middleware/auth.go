package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/shared/httpx"
	"pulsefeed/internal/shared/jwtx"
)

const userIDKey = "user_id"

func Auth(manager *jwtx.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			httpx.Unauthorized(c, "缺少或格式错误的访问令牌")
			return
		}

		userID, err := manager.Parse(strings.TrimSpace(parts[1]))
		if err != nil {
			httpx.Unauthorized(c, "访问令牌无效或已过期")
			return
		}

		c.Set(userIDKey, userID)
		c.Next()
	}
}

func CurrentUserID(c *gin.Context) (int64, bool) {
	value, ok := c.Get(userIDKey)
	if !ok {
		return 0, false
	}
	userID, ok := value.(int64)
	return userID, ok
}
