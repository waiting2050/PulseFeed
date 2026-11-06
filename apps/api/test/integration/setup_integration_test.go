//go:build integration

// setup_integration_test.go 提供集成测试的统一环境：
// 每次运行创建独立的 MySQL schema（跑完即删），并实际连通 Redis 与 RabbitMQ。
//
// 运行方式（在 apps/api 目录下）：
//
//	PULSEFEED_IT_MYSQL_DSN='root:pulsefeed-root@tcp(127.0.0.1:3307)/?charset=utf8mb4&parseTime=True&loc=Local' \
//	PULSEFEED_IT_REDIS_ADDR='127.0.0.1:6380' \
//	PULSEFEED_IT_RABBITMQ_URL='amqp://pulsefeed:pulsefeed@127.0.0.1:5673/' \
//	go test -tags=integration ./test/integration -v -count=1
//
// Windows PowerShell 用 $env:PULSEFEED_IT_MYSQL_DSN='...' 逐条设置。
// 默认值对应 apps/docker-compose.yml 的默认宿主端口；若端口被占用，用临时 .env 换端口后同步覆盖上述环境变量。
// 排障：连接失败时先 `docker compose ps` 确认 mysql/redis/rabbitmq 为 healthy；
// 若报 CREATE DATABASE 权限不足，DSN 需使用 root 账号（默认值即 root）。
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraimpression "pulsefeed/internal/infra/persistence/impression"
	infraplayback "pulsefeed/internal/infra/persistence/playback"
)

const (
	defaultMySQLDSN = "root:pulsefeed-root@tcp(127.0.0.1:3307)/?charset=utf8mb4&parseTime=True&loc=Local"
	defaultRedis    = "127.0.0.1:6380"
	defaultMQURL    = "amqp://pulsefeed:pulsefeed@127.0.0.1:5673/"
)

// testDB 是本次运行独占 schema 的 GORM 连接，所有集成测试共享。
var testDB *gorm.DB

// testSchema 记录独立 schema 名，供断言输出与清理使用。
var testSchema string

func TestMain(m *testing.M) {
	dsn := envOr("PULSEFEED_IT_MYSQL_DSN", defaultMySQLDSN)
	redisAddr := envOr("PULSEFEED_IT_REDIS_ADDR", defaultRedis)
	mqURL := envOr("PULSEFEED_IT_RABBITMQ_URL", defaultMQURL)

	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析 PULSEFEED_IT_MYSQL_DSN 失败: %v\n", err)
		os.Exit(1)
	}
	testSchema = fmt.Sprintf("pulsefeed_it_%d", time.Now().UnixNano())

	serverDB, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接 MySQL 失败（%s）: %v\n", dsn, err)
		os.Exit(1)
	}
	if err := serverDB.Exec("CREATE DATABASE `" + testSchema + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci").Error; err != nil {
		fmt.Fprintf(os.Stderr, "创建独立 schema %s 失败: %v\n", testSchema, err)
		os.Exit(1)
	}

	schemaCfg := *cfg
	schemaCfg.DBName = testSchema
	db, err := gorm.Open(mysql.Open(schemaCfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接独立 schema 失败: %v\n", err)
		os.Exit(1)
	}
	models := []any{
		&infraclip.ClipModel{},
		&infraclip.ClipStatsModel{},
		&infraimpression.PlayEventModel{},
		&infraimpression.ImpressionModel{},
		&infraplayback.QoSReportModel{},
	}
	if err := db.AutoMigrate(models...); err != nil {
		fmt.Fprintf(os.Stderr, "独立 schema 建表失败: %v\n", err)
		os.Exit(1)
	}
	testDB = db

	checkRedis(redisAddr)
	checkRabbitMQ(mqURL)
	fmt.Printf("integration env ready: schema=%s redis=%s rabbitmq=%s\n", testSchema, redisAddr, mqURL)

	code := m.Run()

	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	if err := serverDB.Exec("DROP DATABASE `" + testSchema + "`").Error; err != nil {
		fmt.Fprintf(os.Stderr, "清理 schema %s 失败: %v\n", testSchema, err)
		code = 1
	}
	os.Exit(code)
}

// envOr 读取环境变量，缺失时回退默认值。
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// checkRedis 真实连接并 PING Redis，不可用直接终止测试进程。
func checkRedis(addr string) {
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Redis 不可用（%s）: %v\n", addr, err)
		os.Exit(1)
	}
}

// checkRabbitMQ 真实建立 AMQP 连接后立即关闭，验证 MQ 可达。
func checkRabbitMQ(url string) {
	conn, err := amqp.Dial(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RabbitMQ 不可用（%s）: %v\n", url, err)
		os.Exit(1)
	}
	_ = conn.Close()
}

// uniqueKey 为每次运行生成互不冲突的 idempotency key / event_id。
func uniqueKey(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, testSchema)
}

// seedClip 写入一条可见 clip 并返回其 ID；不创建计数行（LEFT JOIN 缺失按 0 处理）。
func seedClip(t *testing.T, creatorID int64, title string, publishedAt time.Time, status int) int64 {
	t.Helper()
	model := &infraclip.ClipModel{
		CreatorID:      creatorID,
		Title:          title,
		VideoURL:       "https://cdn.example.com/it.mp4",
		Status:         status,
		IdempotencyKey: uniqueKey(title),
		PublishedAt:    publishedAt,
	}
	if err := testDB.Create(model).Error; err != nil {
		t.Fatalf("写入 clip 失败: %v", err)
	}
	return model.ID
}
