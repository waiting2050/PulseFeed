package httpapi

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	accounthttp "pulsefeed/internal/account/http"
	feedhandl "pulsefeed/internal/feed/http"
	interactionhttp "pulsefeed/internal/interaction/http"
	relationhttp "pulsefeed/internal/relation/http"
	"pulsefeed/internal/shared/jwtx"
	"pulsefeed/internal/shared/middleware"
	"pulsefeed/internal/upload"
	videohandl "pulsefeed/internal/video/http"
)

func NewRouter(
	logger *slog.Logger,
	jwtManager *jwtx.Manager,
	accountHandler *accounthttp.Handler,
	videoHandler *videohandl.Handler,
	uploadHandler *upload.Handler,
	feedHandler *feedhandl.Handler,
	interactionHandler *interactionhttp.Handler,
	relationHandler *relationhttp.Handler,
	uploadDir string,
) *gin.Engine {
	router := gin.New()
	router.MaxMultipartMemory = 16 << 20
	router.Use(gin.Recovery(), requestLogger(logger))

	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "pulsefeed"})
	})
	router.Static("/uploads", uploadDir)

	auth := middleware.Auth(jwtManager)
	api := router.Group("/api")
	accountHandler.RegisterRoutes(api, auth)
	videoHandler.RegisterRoutes(api, auth)
	uploadHandler.RegisterRoutes(api, auth)
	feedHandler.RegisterRoutes(api, auth)
	interactionHandler.RegisterRoutes(api, auth)
	relationHandler.RegisterRoutes(api, auth)

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
