// router.go 是组合根：复用 *sql.DB 打开 GORM、执行迁移，装配账号认证链路并注册全部路由。
package router

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"

	appaccount "pulsefeed/internal/application/account"
	appclip "pulsefeed/internal/application/clip"
	appfeed "pulsefeed/internal/application/feed"
	appimpression "pulsefeed/internal/application/impression"
	appaction "pulsefeed/internal/application/interaction"
	apprecommendation "pulsefeed/internal/application/recommendation"
	apprelation "pulsefeed/internal/application/relation"
	infracache "pulsefeed/internal/infra/cache"
	"pulsefeed/internal/infra/config"
	infrajwt "pulsefeed/internal/infra/jwt"
	inframq "pulsefeed/internal/infra/mq"
	infraaccount "pulsefeed/internal/infra/persistence/account"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraembedding "pulsefeed/internal/infra/persistence/embedding"
	infrafeed "pulsefeed/internal/infra/persistence/feed"
	infraimpression "pulsefeed/internal/infra/persistence/impression"
	infraaction "pulsefeed/internal/infra/persistence/interaction"
	"pulsefeed/internal/infra/persistence/migration"
	infrarecommendation "pulsefeed/internal/infra/persistence/recommendation"
	infrarelation "pulsefeed/internal/infra/persistence/relation"
	httpaccount "pulsefeed/internal/interfaces/http/account"
	httpclip "pulsefeed/internal/interfaces/http/clip"
	httpfeed "pulsefeed/internal/interfaces/http/feed"
	httpimpression "pulsefeed/internal/interfaces/http/impression"
	httpaction "pulsefeed/internal/interfaces/http/interaction"
	"pulsefeed/internal/interfaces/http/middleware"
	httprecommendation "pulsefeed/internal/interfaces/http/recommendation"
	httprelation "pulsefeed/internal/interfaces/http/relation"
	httpupload "pulsefeed/internal/interfaces/http/upload"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// Register 完成迁移与依赖装配，并将健康检查、指标与业务路由挂载到给定的 Gin 引擎上。
func Register(engine *gin.Engine, cfg *config.Config, db *sql.DB) error {
	// 复用连接池打开 GORM，避免重复建连；SkipInitializeWithVersion 兼容更多 MySQL 版本。
	gormDB, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("初始化 GORM 失败: %w", err)
	}

	if err := migration.AutoMigrate(gormDB, migration.Models()...); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	log.Printf("migrate done (models=%d)", len(migration.Models()))

	// 认证链路：JWT 签发器 → 仓储 → 应用服务 → 处理器 → 路由。
	issuer, err := infrajwt.New(cfg.JWT.Secret, cfg.JWT.AccessTTL)
	if err != nil {
		return fmt.Errorf("初始化 JWT 签发器失败: %w", err)
	}
	accountRepo := infraaccount.New(gormDB)
	accountService := appaccount.New(accountRepo, appaccount.WithTokenIssuer(issuer))
	accountHandler := httpaccount.New(accountService)

	// MQ 基座：url 为空或连接失败时不阻塞启动，异步链路自动降级为同步落库。
	mqClient, mqErr := inframq.New(cfg.RabbitMQ)
	if mqErr != nil {
		log.Printf("warning: RabbitMQ 初始化失败，异步互动链路降级为同步落库: %v", mqErr)
		mqClient = nil
	} else if mqClient == nil {
		log.Printf("rabbitmq.url 为空，MQ 链路已禁用，互动走同步落库")
	}

	// clip 链路：仓储 → 应用服务 → 处理器；MQ 可用时投递 clip.published 供 S12 扇出消费。
	clipRepo := infraclip.New(gormDB)
	clipOpts := make([]appclip.Option, 0, 1)
	if mqClient != nil {
		clipOpts = append(clipOpts, appclip.WithPublisher(mqClient))
	}
	clipService := appclip.New(clipRepo, clipOpts...)
	clipHandler := httpclip.New(clipService)

	// upload 链路：文件落盘 ./uploads 并经静态路由回读，上传端点要求登录。
	uploadHandler := httpupload.New("./uploads")

	// feed 链路：latest 场景复合游标分页；页缓存 + singleflight 回源，Redis 缺失/禁用自动降级纯 DB。
	redisClient := infracache.New(cfg.Redis)
	feedCache := infracache.NewFeedCache(redisClient)
	if feedCache.Enabled() {
		log.Printf("redis 缓存已启用 (addr=%s db=%d)", cfg.Redis.Addr, cfg.Redis.DB)
	} else {
		log.Printf("redis.addr 为空，缓存已禁用，Feed 走 DB")
	}
	feedRepo := infrafeed.New(gormDB)
	// 向量与推荐链路：embedding 仓储同时服务推荐兴趣向量聚合，Worker 侧负责生成向量。
	embeddingRepo := infraembedding.New(gormDB)
	recommendationRepo := infrarecommendation.New(gormDB)
	recommendationService := apprecommendation.New(recommendationRepo, embeddingRepo)
	// following 场景策略：合并读者 inbox 与大 V outbox，Redis 缺失/禁用自动回源 DB；
	// hot 场景策略：合并最近 60 个分钟桶的互动增量，窗口为空/不可用自动回源 DB。
	feedService := appfeed.New(feedRepo,
		appfeed.WithFeedCache(feedCache),
		appfeed.WithFollowingStrategy(feedCache),
		appfeed.WithForYouStrategy(recommendationService),
		appfeed.WithHotStrategy(feedCache),
	)
	feedHandler := httpfeed.New(feedService)
	recommendationHandler := httprecommendation.New(recommendationService)

	// relation 链路：关注/取关与列表；关注状态迁移时经 feed 回填器同步回填读者 inbox。
	relationRepo := infrarelation.New(gormDB)
	relationService := apprelation.New(relationRepo,
		apprelation.WithFollowFeedBackfiller(appfeed.NewInboxBackfiller(feedRepo, feedCache)),
	)
	relationHandler := httprelation.New(relationService)

	// 互动链路：Redis 与 MQ 均可用时启用异步流水线（Redis 先写 → MQ → Worker 落库），
	// 否则回退 S9 的同步落库路径；评论始终同步处理。
	// 计数真正变化时同步累加 Redis 热度分钟桶（S15 hot 场景数据源），缓存禁用时实现自身跳过。
	actionRepo := infraaction.New(gormDB)
	actionOpts := make([]appaction.Option, 0, 2)
	actionOpts = append(actionOpts, appaction.WithHotScoreRecorder(feedCache))
	if feedCache.Enabled() && mqClient != nil {
		actionOpts = append(actionOpts, appaction.WithAsyncActionPipeline(feedCache, mqClient))
		log.Printf("互动异步链路已启用 (queue=%s)", cfg.RabbitMQ.ActionChangedQueue)
	} else {
		log.Printf("互动异步链路未启用，点赞/收藏走同步落库")
	}
	actionService := appaction.New(actionRepo, actionOpts...)
	actionHandler := httpaction.New(actionService)

	// 曝光链路：播放事件幂等落库 + 当日曝光聚合；MQ 可用时投递 play.recorded 供 Worker 补偿。
	impressionRepo := infraimpression.New(gormDB)
	impressionOpts := make([]appimpression.Option, 0, 1)
	if mqClient != nil {
		impressionOpts = append(impressionOpts, appimpression.WithPublisher(mqClient))
		log.Printf("播放事件异步链路已启用 (queue=%s)", cfg.RabbitMQ.PlayEventRecordedQueue)
	}
	impressionService := appimpression.New(impressionRepo, impressionOpts...)
	impressionHandler := httpimpression.New(impressionService)

	jwtAuth := middleware.NewJWTAuth(issuer)
	optionalAuth := middleware.NewOptionalJWTAuth(issuer)

	api := engine.Group("/api")
	api.POST("/users", accountHandler.Register)
	api.POST("/sessions", accountHandler.Login)
	api.GET("/users/:userId", accountHandler.GetByID)
	api.GET("/users/:userId/clips", clipHandler.ListByCreator)
	api.GET("/clips/:clipId", clipHandler.GetByID)
	// Feed 读取允许未登录，携带合法 token 时回显读者互动状态（S9 已生效）。
	api.GET("/feed", optionalAuth, feedHandler.GetFeed)
	api.POST("/feed-search", optionalAuth, feedHandler.SearchFeed)
	// 评论列表允许匿名读取。
	api.GET("/clips/:clipId/comments", actionHandler.ListComments)

	authed := api.Group("", jwtAuth)
	authed.DELETE("/sessions/current", accountHandler.Logout)
	authed.GET("/users/me", accountHandler.Me)
	authed.PATCH("/users/me", accountHandler.UpdateMe)
	authed.GET("/users/me/clips", clipHandler.ListMine)
	authed.POST("/clips", clipHandler.Publish)
	authed.DELETE("/clips/:clipId", clipHandler.Delete)
	authed.POST("/uploads", uploadHandler.Upload)
	// 互动端点：点赞/收藏状态变更与评论增删，均要求登录。
	authed.PUT("/clips/:clipId/like", actionHandler.Like)
	authed.DELETE("/clips/:clipId/like", actionHandler.Unlike)
	authed.PUT("/clips/:clipId/save", actionHandler.Save)
	authed.DELETE("/clips/:clipId/save", actionHandler.Unsave)
	authed.POST("/clips/:clipId/comments", actionHandler.CreateComment)
	authed.DELETE("/comments/:commentId", actionHandler.DeleteComment)
	// 播放事件上报：登录用户上报一次播放行为，幂等落库并聚合当日曝光。
	authed.POST("/clip-play-events", impressionHandler.RecordPlayEvent)
	// 关注关系端点：关注/取关与关注/粉丝列表，均要求登录。
	authed.PUT("/users/me/following/:targetUserId", relationHandler.Follow)
	authed.DELETE("/users/me/following/:targetUserId", relationHandler.Unfollow)
	authed.GET("/users/me/following", relationHandler.ListFollowing)
	authed.GET("/users/me/followers", relationHandler.ListFollowers)

	// 内部接口：X-Internal-Token 鉴权，供推荐排查与曝光决策核对。
	internal := engine.Group("/internal", middleware.NewInternalTokenAuth(cfg.Internal.Token))
	internal.POST("/recommendation-candidates", recommendationHandler.ListCandidates)
	internal.GET("/impression-decisions", recommendationHandler.ImpressionDecision)
	internal.GET("/impressions", recommendationHandler.ListImpressions)

	// 上传产物静态回读：GET/HEAD /uploads/<kind>/<filename>。
	engine.Static("/uploads", "./uploads")

	registry := prometheus.NewRegistry()

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulsefeed_http_requests_total",
		Help: "HTTP 请求总数，按请求方法与状态码区分。",
	}, []string{"method", "status"})

	serviceUp := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pulsefeed_service_up",
		Help: "服务运行状态，1 表示可用。",
	})
	serviceUp.Set(1)

	registry.MustRegister(requests, serviceUp)

	engine.Use(countRequests(requests))
	engine.GET("/health", health)
	engine.GET("/metrics", gin.WrapH(promhttp.HandlerFor(registry, promhttp.HandlerOpts{})))
	return nil
}

// health 返回服务健康状态。
func health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": gin.H{
			"status": "ok",
		},
	})
}

// countRequests 统计每个请求的方法与响应状态码。
func countRequests(counter *prometheus.CounterVec) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		counter.WithLabelValues(c.Request.Method, strconv.Itoa(c.Writer.Status())).Inc()
	}
}
