package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/shared/httpx"
	"pulsefeed/internal/shared/metrics"
)

// AccessLog 输出结构化访问日志，同时记录请求数、耗时和并发数指标。
func AccessLog(logger *slog.Logger, registry *metrics.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		if registry != nil {
			registry.IncInFlight()
		}
		c.Next()
		elapsed := time.Since(start)

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		if registry != nil {
			registry.DecInFlight()
			registry.ObserveRequest(c.Request.Method, route, c.Writer.Status(), elapsed.Seconds())
		}

		attrs := []any{
			"request_id", RequestIDFrom(c),
			"method", c.Request.Method,
			"route", route,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", elapsed.Milliseconds(),
			"response_bytes", c.Writer.Size(),
			"client_ip", c.ClientIP(),
		}
		if userID, ok := CurrentUserID(c); ok {
			attrs = append(attrs, "user_id", userID)
		}

		if c.Writer.Status() >= http.StatusInternalServerError {
			logger.Error("http request", attrs...)
			return
		}
		logger.Info("http request", attrs...)
	}
}

// Recovery 捕获 panic，记录堆栈并返回统一的错误响应，避免连接被直接断开。
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			logger.Error("http panic",
				"request_id", RequestIDFrom(c),
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"panic", fmt.Sprint(recovered),
				"stack", string(debug.Stack()),
			)
			if !c.Writer.Written() {
				httpx.Internal(c, "服务器内部错误")
			}
			c.Abort()
		}()
		c.Next()
	}
}
