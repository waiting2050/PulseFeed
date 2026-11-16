// router.go 是组合根：复用 *sql.DB 打开 GORM、执行迁移，装配账号认证链路并注册全部路由。
package router

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	appaccount "pulsefeed/internal/application/account"
	appclip "pulsefeed/internal/application/clip"
	appfeed "pulsefeed/internal/application/feed"
	appimpression "pulsefeed/internal/application/impression"
	appaction "pulsefeed/internal/application/interaction"
	appmessage "pulsefeed/internal/application/message"
	appplayback "pulsefeed/internal/application/playback"
	apprecommendation "pulsefeed/internal/application/recommendation"
	apprelation "pulsefeed/internal/application/relation"
	domfeed "pulsefeed/internal/domain/feed"
	domplayback "pulsefeed/internal/domain/playback"
	infracache "pulsefeed/internal/infra/cache"
	"pulsefeed/internal/infra/config"
	infrajwt "pulsefeed/internal/infra/jwt"
	inframetrics "pulsefeed/internal/infra/metrics"
	inframq "pulsefeed/internal/infra/mq"
	infraaccount "pulsefeed/internal/infra/persistence/account"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraembedding "pulsefeed/internal/infra/persistence/embedding"
	infrafeed "pulsefeed/internal/infra/persistence/feed"
	infraimpression "pulsefeed/internal/infra/persistence/impression"
	infraaction "pulsefeed/internal/infra/persistence/interaction"
	inframessage "pulsefeed/internal/infra/persistence/message"
	"pulsefeed/internal/infra/persistence/migration"
	infraplayback "pulsefeed/internal/infra/persistence/playback"
	infrarecommendation "pulsefeed/internal/infra/persistence/recommendation"
	infrarelation "pulsefeed/internal/infra/persistence/relation"
	httpaccount "pulsefeed/internal/interfaces/http/account"
	httpclip "pulsefeed/internal/interfaces/http/clip"
	httpfeed "pulsefeed/internal/interfaces/http/feed"
	httpimpression "pulsefeed/internal/interfaces/http/impression"
	httpaction "pulsefeed/internal/interfaces/http/interaction"
	httpmsg "pulsefeed/internal/interfaces/http/message"
	"pulsefeed/internal/interfaces/http/middleware"
	httpplayback "pulsefeed/internal/interfaces/http/playback"
	httprecommendation "pulsefeed/internal/interfaces/http/recommendation"
	httprelation "pulsefeed/internal/interfaces/http/relation"
	httpupload "pulsefeed/internal/interfaces/http/upload"

	"github.com/gin-gonic/gin"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// Register 完成迁移与依赖装配，并将健康检查、指标与业务路由挂载到给定的 Gin 引擎上。
func Register(engine *gin.Engine, cfg *config.Config, db *sql.DB) error {
	// 可信代理必须在注册路由前设置：只有来自配置地址的 X-Forwarded-For / X-Real-IP
	// 才会被 ClientIP 采信，避免客户端伪造来源 IP 绕过登录/刷新限流。
	if err := engine.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return fmt.Errorf("设置可信代理失败: %w", err)
	}

	// 复用连接池打开 GORM，避免重复建连；SkipInitializeWithVersion 兼容更多 MySQL 版本。
	gormDB, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("初始化 GORM 失败: %w", err)
	}

	if err := migration.AutoMigrate(gormDB, migration.Models()...); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	log.Printf("migrate done (models=%d)", len(migration.Models()))

	// 可观测性：API 进程独立指标注册表，HTTP 中间件与应用服务共用同一实例。
	// 中间件必须在路由注册前启用，Gin 的 Use 不会回溯作用于已注册路由。
	apiMetrics := inframetrics.New("api")
	engine.Use(observeHTTP(apiMetrics))

	// 认证链路在 Redis 就绪后装配：签发器 → 仓储/缓存/限流/拒绝名单 → 应用服务 → 处理器。

	// MQ 基座：url 为空或连接失败时不阻塞启动，异步链路自动降级为同步落库。
	mqClient, mqErr := inframq.New(cfg.RabbitMQ)
	if mqErr != nil {
		log.Printf("warning: RabbitMQ 初始化失败，异步互动链路降级为同步落库: %v", mqErr)
		mqClient = nil
	} else if mqClient == nil {
		log.Printf("rabbitmq.url 为空，MQ 链路已禁用，互动走同步落库")
	}

	// clip 仓储；应用服务在 Feed 缓存装配后创建，以便删除时失效卡片/页缓存。
	clipRepo := infraclip.New(gormDB)

	// upload 链路：文件落盘 ./uploads 并经静态路由回读，上传端点要求登录。
	uploadHandler := httpupload.New("./uploads")

	// feed 链路：latest 场景复合游标分页；页缓存 + singleflight 回源，Redis 缺失/禁用自动降级纯 DB。
	feedRepo := infrafeed.New(gormDB)
	redisClient := infracache.New(cfg.Redis)
	// 互动计数缓存以 pf_clip_stats 为基线：首次访问/TTL 到期时种子化，保证接口计数与 DB 对齐。
	feedCache := infracache.NewFeedCache(redisClient, infracache.WithActionStatsReader(feedRepo))
	if feedCache.Enabled() {
		log.Printf("redis 缓存已启用 (addr=%s db=%d)", cfg.Redis.Addr, cfg.Redis.DB)
	} else {
		log.Printf("redis.addr 为空，缓存已禁用，Feed 走 DB")
	}

	// 认证链路：JWT 签发器 → 刷新令牌仓储/宽限缓存/限流/拒绝名单 → 应用服务 → 处理器。
	issuer, err := infrajwt.New(cfg.JWT.Secret, cfg.JWT.AccessTTL, cfg.JWT.Leeway)
	if err != nil {
		return fmt.Errorf("初始化 JWT 签发器失败: %w", err)
	}
	accountRepo := infraaccount.New(gormDB)
	sessionRepo := infraaccount.NewSession(gormDB)
	rotationCache := infracache.NewRotationCache(redisClient)
	refreshGuard := infracache.NewRefreshGuard(redisClient)
	loginGuard := infracache.NewLoginGuard(redisClient)
	denylist := infracache.NewDenylist(redisClient)
	accountService := appaccount.New(accountRepo,
		appaccount.WithTokenIssuer(issuer),
		appaccount.WithSessionRepository(sessionRepo),
		appaccount.WithRotationCache(rotationCache),
		appaccount.WithRefreshLimiter(refreshGuard),
		appaccount.WithLoginLimiter(loginGuard),
		appaccount.WithAccessDenylist(denylist),
		appaccount.WithMetrics(apiMetrics),
		appaccount.WithRefreshPolicy(cfg.JWT.RefreshTTL, cfg.JWT.RefreshGrace),
		appaccount.WithAccessPolicy(cfg.JWT.Leeway),
	)
	accountHandler := httpaccount.New(accountService)
	// clip 链路：仓储 → 应用服务 → 处理器；MQ 可用时启用事务性 outbox，
	// 事件与 clip 同事务落库后由 Worker 的 relay 投递；删除成功后经 FeedCache 失效缓存。
	clipOpts := make([]appclip.Option, 0, 2)
	if mqClient != nil {
		clipOpts = append(clipOpts, appclip.WithOutbox())
	}
	if feedCache.Enabled() {
		clipOpts = append(clipOpts, appclip.WithFeedCacheInvalidator(feedCache))
	}
	clipService := appclip.New(clipRepo, clipOpts...)
	clipHandler := httpclip.New(clipService)
	// 向量与推荐链路：embedding 仓储同时服务推荐兴趣向量聚合，Worker 侧负责生成向量。
	embeddingRepo := infraembedding.New(gormDB)
	recommendationRepo := infrarecommendation.New(gormDB)
	recommendationService := apprecommendation.New(recommendationRepo, embeddingRepo)
	// following/hot 的请求级限流：Redis 固定窗口，Redis 不可用时 fail-open。
	feedGuard := infracache.NewFeedGuard(redisClient)
	// following 场景策略：合并读者 inbox 与大 V outbox，Redis 缺失/禁用自动回源 DB；
	// hot 场景策略：合并最近 60 个分钟桶的互动增量，窗口为空/不可用自动回源 DB。
	feedService := appfeed.New(feedRepo,
		appfeed.WithFeedCache(feedCache),
		appfeed.WithFollowingStrategy(feedCache),
		appfeed.WithForYouStrategy(recommendationService),
		appfeed.WithHotStrategy(feedCache),
		appfeed.WithFeedRateLimiter(feedGuard),
		// 共享回源与请求生命周期解耦后，需要独立的执行上限（配置 feed.shared_load_timeout）。
		appfeed.WithSharedLoadTimeout(cfg.Feed.SharedLoadTimeout),
		// 游标统一由服务端 HMAC 签名：只有本服务生成的游标才能通过校验，
		// 任意伪造游标无法再构造出无限多的页缓存键。
		appfeed.WithCursorCodec(domfeed.NewCursorCodec(cfg.Feed.CursorSecret)),
		appfeed.WithMetrics(apiMetrics),
	)
	feedHandler := httpfeed.New(feedService)
	recommendationHandler := httprecommendation.New(recommendationService)

	// message 链路：站内消息表、未读数与标记已读；服务同时实现 relation 的 FollowEventMessenger 端口（S16 接入）。
	messageRepo := inframessage.New(gormDB)
	messageService := appmessage.New(messageRepo)
	messageHandler := httpmsg.New(messageService)

	// playback 链路：播放配置（预加载条数/缓冲时长/QoS 采样率）与 QoS 幂等上报；
	// 预加载列表数据源复用 feedRepo.ListLatestClipMedia，通过 adapter 桥接接口签名。
	playbackRepo := infraplayback.New(gormDB)
	playbackService := appplayback.New(playbackRepo, appplayback.ClipProviderFunc(
		func(ctx context.Context, limit int) ([]domplayback.PreloadMedia, error) {
			clips, err := feedRepo.ListLatestClipMedia(ctx, limit)
			if err != nil {
				return nil, err
			}
			items := make([]domplayback.PreloadMedia, 0, len(clips))
			for _, clip := range clips {
				items = append(items, domplayback.PreloadMedia{
					ClipID:     clip.ClipID,
					MediaURL:   clip.MediaURL,
					CoverURL:   clip.CoverURL,
					DurationMS: clip.DurationMS,
				})
			}
			return items, nil
		},
	), appplayback.WithMetrics(apiMetrics))
	playbackHandler := httpplayback.New(playbackService)

	// relation 链路：关注/取关与列表；关注状态迁移时经 feed 回填器同步回填读者 inbox，
	// 取关时清理该创作者在读者 inbox 的残留条目，并经 message service 向被关注者发送站内消息。
	relationRepo := infrarelation.New(gormDB)
	relationService := apprelation.New(relationRepo,
		apprelation.WithFollowFeedBackfiller(appfeed.NewInboxBackfiller(feedRepo, feedCache)),
		apprelation.WithFollowEventMessenger(messageService),
		apprelation.WithInboxCleaner(feedCache),
	)
	relationHandler := httprelation.New(relationService)

	// 互动链路：Redis 与 MQ 均可用时启用异步流水线（Redis 先写 → MQ → Worker 落库），
	// 否则回退 S9 的同步落库路径；评论始终同步处理。
	// 热度分钟桶属于非正确性写入：同步链路在事务提交后累加，异步链路由 Worker 在落库成功后累加。
	actionRepo := infraaction.New(gormDB)
	actionOpts := make([]appaction.Option, 0, 4)
	actionOpts = append(actionOpts, appaction.WithHotScoreRecorder(feedCache))
	// 提交后副作用失败（热度/通知/事件投递）只计数不回滚，指标供告警与补偿。
	actionOpts = append(actionOpts, appaction.WithMetrics(apiMetrics))
	// 点赞/评论通知：message 服务作为投递端口，创作者通过 clip 仓储的最小解析器获得。
	actionOpts = append(actionOpts, appaction.WithMessageNotifier(messageService, appaction.ClipOwnerFunc(
		func(ctx context.Context, clipID int64) (int64, error) {
			clip, err := clipRepo.GetByID(ctx, clipID)
			if err != nil {
				return 0, err
			}
			return clip.CreatorID, nil
		},
	)))
	if feedCache.Enabled() && mqClient != nil {
		actionOpts = append(actionOpts, appaction.WithAsyncActionPipeline(feedCache, mqClient))
		log.Printf("互动异步链路已启用 (queue=%s)", cfg.RabbitMQ.ActionChangedQueue)
	} else {
		log.Printf("互动异步链路未启用，点赞/收藏走同步落库")
	}
	actionService := appaction.New(actionRepo, actionOpts...)
	actionHandler := httpaction.New(actionService)

	// 曝光链路：播放事件幂等落库 + 当日曝光聚合；MQ 可用时启用事务性 outbox，
	// play.recorded 与播放事件同事务落库后由 Worker 的 relay 投递。
	impressionRepo := infraimpression.New(gormDB)
	impressionOpts := make([]appimpression.Option, 0, 1)
	if mqClient != nil {
		impressionOpts = append(impressionOpts, appimpression.WithOutbox())
		log.Printf("播放事件异步链路已启用 outbox (queue=%s)", cfg.RabbitMQ.PlayEventRecordedQueue)
	}
	impressionService := appimpression.New(impressionRepo, impressionOpts...)
	impressionHandler := httpimpression.New(impressionService)

	jwtAuth := middleware.NewJWTAuth(issuer, denylist, apiMetrics)
	optionalAuth := middleware.NewOptionalJWTAuth(issuer, denylist, apiMetrics)

	api := engine.Group("/api")
	api.POST("/users", accountHandler.Register)
	api.POST("/sessions", accountHandler.Login)
	api.POST("/sessions/refresh", accountHandler.Refresh)
	api.GET("/users/:userId", accountHandler.GetByID)
	api.GET("/users/:userId/clips", clipHandler.ListByCreator)
	// 公开主页的关系概览与关注/粉丝列表：允许匿名读取，带 token 时回显当前读者是否已关注。
	api.GET("/users/:userId/relation", optionalAuth, relationHandler.GetStats)
	api.GET("/users/:userId/following", relationHandler.ListFollowingOf)
	api.GET("/users/:userId/followers", relationHandler.ListFollowersOf)
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
	// 站内消息端点：列表、未读数与按条/全部标记已读，均要求登录（S16 接入）。
	authed.GET("/messages", messageHandler.ListMessages)
	authed.GET("/message-stats/unread", messageHandler.GetUnreadCount)
	authed.PATCH("/messages", messageHandler.MarkRead)
	// 播放配置与 QoS 上报端点：登录用户读取/更新配置、取预加载列表、幂等上报 QoS（S16 接入）。
	authed.GET("/playback-config", playbackHandler.GetConfig)
	authed.PUT("/playback-config", playbackHandler.UpdateConfig)
	authed.GET("/preload-clips", playbackHandler.GetPreload)
	authed.POST("/playback-qos-reports", playbackHandler.ReportQoS)

	// 内部接口：X-Internal-Token 鉴权，供推荐排查、曝光决策、向量链路核对与消息/QoS 主动投递（S16 接入）。
	internal := engine.Group("/internal", middleware.NewInternalTokenAuth(cfg.Internal.Token))
	internal.POST("/recommendation-candidates", recommendationHandler.ListCandidates)
	internal.GET("/impression-decisions", recommendationHandler.ImpressionDecision)
	internal.GET("/impressions", recommendationHandler.ListImpressions)
	internal.POST("/messages", messageHandler.CreateMessageInternal)
	internal.POST("/playback-qos-reports", playbackHandler.ReportQoSInternal)

	// 上传产物静态回读：GET/HEAD /uploads/<kind>/<filename>。
	engine.Static("/uploads", "./uploads")

	engine.GET("/health", health)
	engine.GET("/metrics", gin.WrapH(apiMetrics.Handler()))
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

// observeHTTP 记录每个请求的方法、状态码与耗时。
func observeHTTP(metrics *inframetrics.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		metrics.ObserveHTTP(c.Request.Method, strconv.Itoa(c.Writer.Status()), time.Since(start))
	}
}
