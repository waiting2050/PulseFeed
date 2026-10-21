package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const defaultJWTSecret = "change-me-in-production-please"

type Config struct {
	AppEnv        string
	AppVersion    string
	HTTPAddr      string
	MySQLDSN      string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	JWTSecret     string
	JWTTTL        time.Duration
	UploadDir     string
	// RateLimitPerMinute 为每个客户端每分钟允许的请求数，0 表示关闭限流。
	RateLimitPerMinute int
}

func Load() (*Config, error) {
	redisDB, err := strconv.Atoi(env("REDIS_DB", "0"))
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_DB: %w", err)
	}

	jwtTTL, err := time.ParseDuration(env("JWT_TTL", "24h"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_TTL: %w", err)
	}

	rateLimit, err := strconv.Atoi(env("RATE_LIMIT_PER_MINUTE", "120"))
	if err != nil || rateLimit < 0 {
		return nil, fmt.Errorf("invalid RATE_LIMIT_PER_MINUTE")
	}

	cfg := &Config{
		AppEnv:             env("APP_ENV", "dev"),
		AppVersion:         env("APP_VERSION", "dev"),
		HTTPAddr:           env("HTTP_ADDR", ":8080"),
		MySQLDSN:           env("MYSQL_DSN", "root:root@tcp(127.0.0.1:3306)/pulsefeed?charset=utf8mb4&parseTime=True&loc=Local"),
		RedisAddr:          env("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:      env("REDIS_PASSWORD", ""),
		RedisDB:            redisDB,
		JWTSecret:          env("JWT_SECRET", defaultJWTSecret),
		JWTTTL:             jwtTTL,
		UploadDir:          env("UPLOAD_DIR", "uploads"),
		RateLimitPerMinute: rateLimit,
	}

	if cfg.AppEnv == "prod" && cfg.JWTSecret == defaultJWTSecret {
		return nil, fmt.Errorf("JWT_SECRET must be set in production")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
