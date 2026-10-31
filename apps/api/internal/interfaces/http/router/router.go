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
	"pulsefeed/internal/infra/config"
	infrajwt "pulsefeed/internal/infra/jwt"
	infraaccount "pulsefeed/internal/infra/persistence/account"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	"pulsefeed/internal/infra/persistence/migration"
	httpaccount "pulsefeed/internal/interfaces/http/account"
	httpclip "pulsefeed/internal/interfaces/http/clip"
	"pulsefeed/internal/interfaces/http/middleware"

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

	if err := migration.AutoMigrate(gormDB, migrateModels()...); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	log.Printf("migrate done (models=%d)", len(migrateModels()))

	// 认证链路：JWT 签发器 → 仓储 → 应用服务 → 处理器 → 路由。
	issuer, err := infrajwt.New(cfg.JWT.Secret, cfg.JWT.AccessTTL)
	if err != nil {
		return fmt.Errorf("初始化 JWT 签发器失败: %w", err)
	}
	accountRepo := infraaccount.New(gormDB)
	accountService := appaccount.New(accountRepo, appaccount.WithTokenIssuer(issuer))
	accountHandler := httpaccount.New(accountService)

	// clip 链路：仓储 → 应用服务 → 处理器；clip.published 事件发布器待 S10 MQ 基座接入。
	clipRepo := infraclip.New(gormDB)
	clipService := appclip.New(clipRepo)
	clipHandler := httpclip.New(clipService)

	jwtAuth := middleware.NewJWTAuth(issuer)

	api := engine.Group("/api")
	api.POST("/users", accountHandler.Register)
	api.POST("/sessions", accountHandler.Login)
	api.GET("/users/:userId", accountHandler.GetByID)
	api.GET("/users/:userId/clips", clipHandler.ListByCreator)
	api.GET("/clips/:clipId", clipHandler.GetByID)

	authed := api.Group("", jwtAuth)
	authed.DELETE("/sessions/current", accountHandler.Logout)
	authed.GET("/users/me", accountHandler.Me)
	authed.PATCH("/users/me", accountHandler.UpdateMe)
	authed.GET("/users/me/clips", clipHandler.ListMine)
	authed.POST("/clips", clipHandler.Publish)
	authed.DELETE("/clips/:clipId", clipHandler.Delete)

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

// migrateModels 返回参与 AutoMigrate 的模型切片；各阶段在此登记新表模型。
func migrateModels() []any {
	return []any{
		&infraaccount.UserModel{},
		&infraaccount.UserProfileModel{},
		&infraclip.ClipModel{},
		&infraclip.ClipStatsModel{},
	}
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
