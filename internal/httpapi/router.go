package httpapi

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	accounthttp "pulsefeed/internal/account/http"
	"pulsefeed/internal/shared/jwtx"
	"pulsefeed/internal/shared/middleware"
)

func NewRouter(logger *slog.Logger, jwtManager *jwtx.Manager, accountHandler *accounthttp.Handler) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery(), requestLogger(logger))

	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "pulsefeed"})
	})

	api := router.Group("/api")
	accountHandler.RegisterRoutes(api, middleware.Auth(jwtManager))

	return router
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
