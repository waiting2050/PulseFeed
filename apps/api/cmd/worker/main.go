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
	appdlq "pulsefeed/internal/application/dlq"
	appembedding "pulsefeed/internal/application/embedding"
	appimpression "pulsefeed/internal/application/impression"
	appaction "pulsefeed/internal/application/interaction"
	appoutbox "pulsefeed/internal/application/outbox"
	infracache "pulsefeed/internal/infra/cache"
	"pulsefeed/internal/infra/config"
	infradatabase "pulsefeed/internal/infra/database"
	inframetrics "pulsefeed/internal/infra/metrics"
	inframq "pulsefeed/internal/infra/mq"
	infradlq "pulsefeed/internal/infra/persistence/dlq"
	infraembedding "pulsefeed/internal/infra/persistence/embedding"
	infrafeed "pulsefeed/internal/infra/persistence/feed"
	infraimpression "pulsefeed/internal/infra/persistence/impression"
	infraaction "pulsefeed/internal/infra/persistence/interaction"
	"pulsefeed/internal/infra/persistence/migration"
	infraoutbox "pulsefeed/internal/infra/persistence/outbox"

	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	metricsAddr := flag.String("metrics-addr", ":9092", "Worker 独立指标监听地址，9092 被系统保留时可改用 :9093")
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

	// 指标注册表与 API 进程隔离；监听地址可由 -metrics-addr 覆盖（Windows 端口保留段场景）。
	workerMetrics := inframetrics.New("worker")

	// 发布扇出与互动热度共用同一个 Redis 缓存实现。
	redisClient := infracache.New(cfg.Redis)
	feedCache := infracache.NewFeedCache(redisClient)

	// 互动落库 Worker：热度分钟桶在 DB 提交成功后累加，写入失败只记日志与指标，
	// 不影响消息 ack（DB 状态已幂等，重投也无法补写热度）。
	actionWorker := appaction.NewActionWorker(infraaction.New(gormDB),
		appaction.WithActionHotScoreRecorder(feedCache),
		appaction.WithActionWorkerMetrics(workerMetrics),
	)

	// 播放事件消费链路：对 API 同步链路未完成曝光聚合的事件做二次 upsert 补偿（D7 修正）。
	playEventWorker := appimpression.NewPlayEventWorker(infraimpression.New(gormDB))

	// 向量生成链路：消费 clip.embedding 事件，按标题 hash n-gram 写入 pf_clip_embeddings。
	embeddingWorker := appembedding.NewWorker(appembedding.New(infraembedding.New(gormDB)))

	// 发布扇出链路：把新 clip 推入粉丝 inbox 或大 V outbox。
	fanoutWorker := appclip.NewFanoutWorker(infrafeed.New(gormDB), feedCache)

	// 事务性 outbox relay：把与 clip 同事务落库的事件经 publisher confirm 投递到 broker，
	// 投递失败按退避重试；重复投递由下游的 event_id 幂等写保证不产生重复业务效果。
	outboxRelay := appoutbox.NewRelay(infraoutbox.New(gormDB), mqClient, map[string]appoutbox.Route{
		appclip.TopicClipPublished: {
			Exchange:   cfg.RabbitMQ.ClipExchange,
			RoutingKey: cfg.RabbitMQ.ClipPublishedRouting,
		},
		appclip.TopicClipEmbedding: {
			Exchange:   cfg.RabbitMQ.ClipExchange,
			RoutingKey: inframq.EmbeddingRouting,
		},
		// 播放事件与 clip 同走 outbox：落库成功即由 relay 经 confirm 投递，重复投递按 event_id 幂等。
		appimpression.TopicPlayRecorded: {
			Exchange:   cfg.RabbitMQ.ImpressionExchange,
			RoutingKey: cfg.RabbitMQ.PlayEventRecordedRouting,
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := inframetrics.RunServer(*metricsAddr, workerMetrics.Registry()); err != nil {
			log.Printf("Worker metrics 端口异常退出: %v", err)
		}
	}()

	// 死信消费者：把四个 .dead 队列中的消息落库并记录日志/指标，
	// 消费失败会重新入队，保证死信不会“只声明不消费”而静默堆积。
	dlqConsumer := appdlq.New(infradlq.New(gormDB), workerMetrics, map[string]string{
		cfg.RabbitMQ.ClipPublishedQueue:     appclip.TopicClipPublished,
		cfg.RabbitMQ.ClipEmbeddingQueue:     appclip.TopicClipEmbedding,
		cfg.RabbitMQ.ActionChangedQueue:     appaction.TopicActionChanged,
		cfg.RabbitMQ.PlayEventRecordedQueue: appimpression.TopicPlayRecorded,
	})
	handleDeadLetter := func(ctx context.Context, message inframq.DeadLetter) error {
		return dlqConsumer.Handle(ctx, appdlq.Message{
			SourceQueue: message.SourceQueue,
			Reason:      message.Reason,
			Attempts:    message.Attempts,
			LastError:   message.LastError,
			MessageID:   message.MessageID,
			Payload:     message.Payload,
		})
	}

	// 四个消费者各自持有独立 AMQP 通道并发消费，outbox relay 独立轮询；任一消费者异常退出即终止进程。
	log.Printf("worker 已启动: %s (queue=%s)", actionWorker.Name(), cfg.RabbitMQ.ActionChangedQueue)
	log.Printf("worker 已启动: %s (queue=%s)", fanoutWorker.Name(), cfg.RabbitMQ.ClipPublishedQueue)
	log.Printf("worker 已启动: %s (queue=%s)", embeddingWorker.Name(), cfg.RabbitMQ.ClipEmbeddingQueue)
	log.Printf("worker 已启动: %s (queue=%s)", playEventWorker.Name(), cfg.RabbitMQ.PlayEventRecordedQueue)
	log.Printf("worker 已启动: outbox_relay")
	errCh := make(chan error, 9)
	go func() {
		errCh <- outboxRelay.Run(ctx)
	}()
	go func() {
		errCh <- mqClient.Consume(ctx, cfg.RabbitMQ.ActionChangedQueue,
			observeWorker(workerMetrics, actionWorker.Name(), actionWorker.Handle))
	}()
	go func() {
		errCh <- mqClient.Consume(ctx, cfg.RabbitMQ.ClipPublishedQueue,
			observeWorker(workerMetrics, fanoutWorker.Name(), fanoutWorker.Handle))
	}()
	go func() {
		errCh <- mqClient.Consume(ctx, cfg.RabbitMQ.ClipEmbeddingQueue,
			observeWorker(workerMetrics, embeddingWorker.Name(), embeddingWorker.Handle))
	}()
	go func() {
		errCh <- mqClient.Consume(ctx, cfg.RabbitMQ.PlayEventRecordedQueue,
			observeWorker(workerMetrics, playEventWorker.Name(), playEventWorker.Handle))
	}()

	// 四组死信队列与业务消费者一一对应；死信消费异常退出同样终止进程，避免告警链路失效。
	deadQueues := []struct {
		queue    string
		exchange string
		routing  string
	}{
		{cfg.RabbitMQ.ClipPublishedQueue, cfg.RabbitMQ.ClipExchange, cfg.RabbitMQ.ClipPublishedRouting},
		{cfg.RabbitMQ.ClipEmbeddingQueue, cfg.RabbitMQ.ClipExchange, inframq.EmbeddingRouting},
		{cfg.RabbitMQ.ActionChangedQueue, cfg.RabbitMQ.InteractionExchange, cfg.RabbitMQ.ActionChangedRouting},
		{cfg.RabbitMQ.PlayEventRecordedQueue, cfg.RabbitMQ.ImpressionExchange, cfg.RabbitMQ.PlayEventRecordedRouting},
	}
	for _, item := range deadQueues {
		_, deadQueue, _ := inframq.DeadLetterNames(item.exchange, item.queue, item.routing)
		log.Printf("worker 已启动: dlq (queue=%s)", deadQueue)
		go func(deadQueue string) {
			errCh <- mqClient.ConsumeDeadLetters(ctx, deadQueue, handleDeadLetter)
		}(deadQueue)
	}
	if err := <-errCh; err != nil {
		log.Fatalf("Worker 消费异常退出: %v", err)
	}

	log.Printf("worker 收到退出信号，已停止消费（metrics 端口 %s）", *metricsAddr)
}

// observeWorker 包装消息处理器，按 Worker 名称记录 ok/error 计数。
func observeWorker(metrics *inframetrics.Metrics, name string, handler func(context.Context, []byte) error) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		err := handler(ctx, body)
		result := "ok"
		if err != nil {
			result = "error"
		}
		metrics.ObserveWorker(name, result)
		return err
	}
}
