package httpapi

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	accounthttp "pulsefeed/internal/account/http"
	feedhandl "pulsefeed/internal/feed/http"
	interactionhttp "pulsefeed/internal/interaction/http"
	messagehttp "pulsefeed/internal/message/http"
	recommendhttp "pulsefeed/internal/recommend/http"
	relationhttp "pulsefeed/internal/relation/http"
	"pulsefeed/internal/shared/jwtx"
	"pulsefeed/internal/shared/metrics"
	"pulsefeed/internal/shared/middleware"
	"pulsefeed/internal/upload"
	videohandl "pulsefeed/internal/video/http"
)

// Deps 汇总路由所需的依赖，避免构造函数参数继续膨胀。
type Deps struct {
	Logger             *slog.Logger
	JWT                *jwtx.Manager
	Metrics            *metrics.Registry
	Database           *sql.DB
	Redis              *redis.Client
	UploadDir          string
	WebDir             string
	RateLimitPerMinute int

	AccountHandler     *accounthttp.Handler
	VideoHandler       *videohandl.Handler
	UploadHandler      *upload.Handler
	FeedHandler        *feedhandl.Handler
	InteractionHandler *interactionhttp.Handler
	RelationHandler    *relationhttp.Handler
	RecommendHandler   *recommendhttp.Handler
	MessageHandler     *messagehttp.Handler
}

func NewRouter(d Deps) *gin.Engine {
	router := gin.New()
	// 不信任任何代理头，避免伪造 X-Forwarded-For 影响限流维度和日志。
	_ = router.SetTrustedProxies(nil)
	router.MaxMultipartMemory = 16 << 20
	router.Use(middleware.RequestID(), middleware.AccessLog(d.Logger, d.Metrics), middleware.Recovery(d.Logger))

	dependencies := []Dependency{mysqlDependency(d.Database), redisDependency(d.Redis)}
	router.GET("/health", liveness())
	router.GET("/ready", readiness(dependencies, d.Metrics, d.Logger))
	if d.Metrics != nil {
		router.GET("/metrics", gin.WrapH(d.Metrics.Handler()))
	}
	router.Static("/uploads", d.UploadDir)
	if d.WebDir != "" {
		router.Static("/web", d.WebDir)
		router.GET("/", func(c *gin.Context) {
			c.File(filepath.Join(d.WebDir, "index.html"))
		})
	}

	auth := middleware.Auth(d.JWT)
	api := router.Group("/api")
	api.Use(middleware.RateLimit(middleware.RateLimitOptions{
		Counter: middleware.NewRedisCounter(d.Redis),
		Limit:   d.RateLimitPerMinute,
		Window:  time.Minute,
		Scope:   "api",
		Logger:  d.Logger,
		Metrics: d.Metrics,
	}))

	d.AccountHandler.RegisterRoutes(api, auth)
	d.VideoHandler.RegisterRoutes(api, auth)
	d.UploadHandler.RegisterRoutes(api, auth)
	d.FeedHandler.RegisterRoutes(api, auth)
	d.InteractionHandler.RegisterRoutes(api, auth)
	d.RelationHandler.RegisterRoutes(api, auth)
	d.RecommendHandler.RegisterRoutes(api, auth)
	d.MessageHandler.RegisterRoutes(api, auth)

	return router
}
