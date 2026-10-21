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
	"pulsefeed/internal/shared/cache"
	"pulsefeed/internal/shared/config"
	"pulsefeed/internal/shared/database"
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
	feedService := feedapp.NewService(feedRepo, feedCache, feedHotStore)
	feedHandler := feedhandl.NewHandler(feedService, log)

	interactionRepo := interactioninfra.NewGormRepository(db)
	interactionService := interactionapp.NewService(interactionRepo, feedHotStore)
	interactionHandler := interactionhandl.NewHandler(interactionService, log)

	router := httpapi.NewRouter(log, jwtManager, accountHandler, videoHandler, uploadHandler, feedHandler, interactionHandler, cfg.UploadDir)
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
