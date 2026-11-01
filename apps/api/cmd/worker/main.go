// main.go 是异步 Worker 的主入口：校验配置、迁移数据库、连接 MQ、启动指标端口与 ActionWorker，并响应信号退出。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	appclip "pulsefeed/internal/application/clip"
	appaction "pulsefeed/internal/application/interaction"
	infracache "pulsefeed/internal/infra/cache"
	"pulsefeed/internal/infra/config"
	infradatabase "pulsefeed/internal/infra/database"
	inframetrics "pulsefeed/internal/infra/metrics"
	inframq "pulsefeed/internal/infra/mq"
	infrafeed "pulsefeed/internal/infra/persistence/feed"
	infraaction "pulsefeed/internal/infra/persistence/interaction"
	"pulsefeed/internal/infra/persistence/migration"

	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// workerMetricsAddr 是 Worker 独立指标端口，与 API 的 8090 分离。
const workerMetricsAddr = ":9092"

func main() {
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	// Worker 强依赖 MQ 与 Redis：缺失任一项直接退出，不带着残缺链路空转。
	if cfg.RabbitMQ.URL == "" {
		log.Fatalf("Worker 启动失败: rabbitmq.url 为空，异步链路需要 MQ")
	}
	if cfg.Redis.Addr == "" {
		log.Fatalf("Worker 启动失败: redis.addr 为空，异步链路需要 Redis")
	}

	db, err := infradatabase.New(cfg.Database)
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		log.Fatalf("初始化 GORM 失败: %v", err)
	}
	if err := migration.AutoMigrate(gormDB, migration.Models()...); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	log.Printf("worker migrate done (models=%d)", len(migration.Models()))

	mqClient, err := inframq.New(cfg.RabbitMQ)
	if err != nil {
		log.Fatalf("RabbitMQ 初始化失败: %v", err)
	}
	defer mqClient.Close()

	actionWorker := appaction.NewActionWorker(infraaction.New(gormDB))

	// 发布扇出链路：复用与 API 相同的 Redis 缓存实现，把新 clip 推入粉丝 inbox 或大 V outbox。
	redisClient := infracache.New(cfg.Redis)
	feedCache := infracache.NewFeedCache(redisClient)
	fanoutWorker := appclip.NewFanoutWorker(infrafeed.New(gormDB), feedCache)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := inframetrics.RunServer(workerMetricsAddr); err != nil {
			log.Printf("Worker metrics 端口异常退出: %v", err)
		}
	}()

	// 两个消费者各自持有独立 AMQP 通道并发消费；任一异常退出即终止进程。
	log.Printf("worker 已启动: %s (queue=%s)", actionWorker.Name(), cfg.RabbitMQ.ActionChangedQueue)
	log.Printf("worker 已启动: %s (queue=%s)", fanoutWorker.Name(), cfg.RabbitMQ.ClipPublishedQueue)
	errCh := make(chan error, 2)
	go func() {
		errCh <- mqClient.Consume(ctx, cfg.RabbitMQ.ActionChangedQueue, actionWorker.Handle)
	}()
	go func() {
		errCh <- mqClient.Consume(ctx, cfg.RabbitMQ.ClipPublishedQueue, fanoutWorker.Handle)
	}()
	if err := <-errCh; err != nil {
		log.Fatalf("Worker 消费异常退出: %v", err)
	}

	log.Printf("worker 收到退出信号，已停止消费（metrics 端口 %s）", workerMetricsAddr)
}
