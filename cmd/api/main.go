package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	accountapp "pulsefeed/internal/account/app"
	accounthttp "pulsefeed/internal/account/http"
	accountinfra "pulsefeed/internal/account/infra"
	feedapp "pulsefeed/internal/feed/app"
	feedhandl "pulsefeed/internal/feed/http"
	feedinfra "pulsefeed/internal/feed/infra"
	"pulsefeed/internal/httpapi"
	interactionapp "pulsefeed/internal/interaction/app"
	interactionhandl "pulsefeed/internal/interaction/http"
	interactioninfra "pulsefeed/internal/interaction/infra"
	messageapp "pulsefeed/internal/message/app"
	messagehandl "pulsefeed/internal/message/http"
	messageinfra "pulsefeed/internal/message/infra"
	recommendapp "pulsefeed/internal/recommend/app"
	recommenddomain "pulsefeed/internal/recommend/domain"
	recommendhandl "pulsefeed/internal/recommend/http"
	recommendinfra "pulsefeed/internal/recommend/infra"
	relationapp "pulsefeed/internal/relation/app"
	relationhandl "pulsefeed/internal/relation/http"
	relationinfra "pulsefeed/internal/relation/infra"
	"pulsefeed/internal/shared/cache"
	"pulsefeed/internal/shared/config"
	"pulsefeed/internal/shared/database"
	"pulsefeed/internal/shared/event"
	"pulsefeed/internal/shared/jwtx"
	"pulsefeed/internal/shared/logger"
	"pulsefeed/internal/upload"
	videoapp "pulsefeed/internal/video/app"
	videohandl "pulsefeed/internal/video/http"
	videoinfra "pulsefeed/internal/video/infra"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.AppEnv)
	if cfg.AppEnv == "prod" {
		gin.SetMode(gin.ReleaseMode)
	}

	db, err := database.OpenMySQL(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	if err := db.AutoMigrate(
		&accountinfra.UserModel{},
		&videoinfra.VideoModel{},
		&videoinfra.VideoStatModel{},
		&interactioninfra.ActionModel{},
		&interactioninfra.CommentModel{},
		&relationinfra.RelationModel{},
		&messageinfra.NotificationModel{},
	); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}

	rdb, err := cache.OpenRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		if cfg.AppEnv == "prod" {
			return err
		}
		log.Warn("redis unavailable, cache features are disabled", "error", err)
	} else {
		defer rdb.Close()
	}

	bus := event.NewBus()
	jwtManager := jwtx.NewManager(cfg.JWTSecret, cfg.JWTTTL)
	accountRepo := accountinfra.NewGormRepository(db)
	accountService := accountapp.NewService(accountRepo, jwtManager)
	accountHandler := accounthttp.NewHandler(accountService, log)

	videoRepo := videoinfra.NewGormRepository(db)
	videoService := videoapp.NewService(videoRepo)
	videoHandler := videohandl.NewHandler(videoService, log)
	uploadHandler := upload.NewHandler(cfg.UploadDir, log)

	feedRepo := feedinfra.NewGormRepository(db)
	feedCache := feedinfra.NewPageCache(rdb, log)
	feedHotStore := feedinfra.NewHotStore(rdb)
	relationRepo := relationinfra.NewGormRepository(db)
	relationService := relationapp.NewService(relationRepo, bus)
	feedService := feedapp.NewService(feedRepo, feedCache, feedHotStore, relationService)
	feedHandler := feedhandl.NewHandler(feedService, log)

	interactionRepo := interactioninfra.NewGormRepository(db)
	interactionService := interactionapp.NewService(interactionRepo, feedHotStore, bus)
	interactionHandler := interactionhandl.NewHandler(interactionService, log)

	relationHandler := relationhandl.NewHandler(relationService, log)

	messageRepo := messageinfra.NewGormRepository(db)
	messageService := messageapp.NewService(messageRepo)
	bus.Subscribe(func(ctx context.Context, evt event.Event) {
		if err := messageService.HandleEvent(ctx, evt); err != nil {
			log.Error("notification event failed", "type", evt.Type, "error", err)
		}
	})
	messageHandler := messagehandl.NewHandler(messageService, log)

	recommendExposed := recommendinfra.NewExposedStore(rdb)
	recommendService := recommendapp.NewService([]recommenddomain.RecallSource{
		recommendinfra.NewHotRecall(feedHotStore),
		recommendinfra.NewFollowingRecall(relationService, feedRepo),
		recommendinfra.NewTimelineRecall(feedRepo),
	}, feedRepo, recommendExposed)
	recommendHandler := recommendhandl.NewHandler(recommendService, log)

	router := httpapi.NewRouter(log, jwtManager, accountHandler, videoHandler, uploadHandler, feedHandler, interactionHandler, relationHandler, recommendHandler, messageHandler, cfg.UploadDir)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	log.Info("pulsefeed api started", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	case sig := <-quit:
		log.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}

	sqlDB, err := db.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
	return nil
}
