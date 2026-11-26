// relay.go 实现事务性 outbox 的投递端：轮询待发送事件，经 publisher confirm 投递到 broker，
// 只有确认成功才标记已发送；失败按指数退避重试，达到上限标记 failed 等待人工处理。
package outbox

import (
	"context"
	"fmt"
	"log"
	"time"
)

const (
	// MaxPublishAttempts 是单条事件的投递尝试上限，达到后标记 failed 并告警，避免无限重试。
	MaxPublishAttempts = 20
	// pollInterval 是 relay 空闲时的轮询间隔。
	pollInterval = time.Second
	// batchSize 是单轮最多处理的事件数，避免一次拉取过多阻塞退出。
	batchSize = 50
	// maxBackoff 是失败重试的最长退避间隔。
	maxBackoff = time.Minute
)

// Route 描述逻辑事件类型对应的交换机与路由键。
type Route struct {
	Exchange   string
	RoutingKey string
}

// Event 是 relay 视角的待投递事件。
type Event struct {
	ID       int64
	EventID  string
	Topic    string
	Payload  []byte
	Attempts int
}

// Store 是 relay 依赖的 outbox 存储端口，由 infra/persistence/outbox 实现。
type Store interface {
	// ListPending 返回 next_attempt_at 已到期、按写入顺序排列的待投递事件。
	ListPending(ctx context.Context, now time.Time, limit int) ([]Event, error)
	// MarkPublished 标记事件已确认投递。
	MarkPublished(ctx context.Context, id int64, publishedAt time.Time) error
	// MarkFailed 记录一次失败；exhausted 为 true 时标记为 failed 并停止重试。
	MarkFailed(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, reason string, exhausted bool) error
}

// Publisher 是消息投递端口，由 infra/mq 实现；返回 nil 表示 broker 已确认接收。
type Publisher interface {
	PublishRaw(ctx context.Context, exchange, routing, messageID string, payload []byte) error
}

// Relay 负责把 outbox 中的事件投递到 MQ。
type Relay struct {
	store     Store
	publisher Publisher
	routes    map[string]Route
	interval  time.Duration
	batch     int
	now       func() time.Time
}

// NewRelay 创建 relay；routes 缺失的 topic 会被标记为 failed 并记录错误日志。
func NewRelay(store Store, publisher Publisher, routes map[string]Route) *Relay {
	return &Relay{
		store:     store,
		publisher: publisher,
		routes:    routes,
		interval:  pollInterval,
		batch:     batchSize,
		now:       time.Now,
	}
}

// Run 启动轮询循环，ctx 取消后返回。
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		if err := r.Drain(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("warning: outbox relay 本轮派发失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Drain 处理一批待投递事件；发布失败时保留事件并按退避重试。
// 重复投递不会破坏业务：消息携带稳定的 event_id，下游扇出使用 ZADD、向量使用 upsert，
// 同一事件多次到达只产生一次业务效果。
func (r *Relay) Drain(ctx context.Context) error {
	now := r.now()
	events, err := r.store.ListPending(ctx, now, r.batch)
	if err != nil {
		return fmt.Errorf("读取待投递 outbox 事件失败: %w", err)
	}

	for _, event := range events {
		route, ok := r.routes[event.Topic]
		if !ok {
			reason := fmt.Sprintf("未配置 topic %q 的投递路由", event.Topic)
			log.Printf("error: outbox 事件 %s 无法投递，标记为 failed: %s", event.EventID, reason)
			if err := r.store.MarkFailed(ctx, event.ID, event.Attempts+1, now, reason, true); err != nil {
				return fmt.Errorf("标记 outbox 事件 %s 失败: %w", event.EventID, err)
			}
			continue
		}

		if err := r.publisher.PublishRaw(ctx, route.Exchange, route.RoutingKey, event.EventID, event.Payload); err != nil {
			attempts := event.Attempts + 1
			exhausted := attempts >= MaxPublishAttempts
			backoff := retryBackoff(attempts)
			if exhausted {
				log.Printf("error: outbox 事件 %s 投递失败已达上限 %d，标记为 failed: %v",
					event.EventID, MaxPublishAttempts, err)
			} else {
				log.Printf("warning: outbox 事件 %s 投递失败 %d/%d，%s 后重试: %v",
					event.EventID, attempts, MaxPublishAttempts, backoff, err)
			}
			if markErr := r.store.MarkFailed(ctx, event.ID, attempts, now.Add(backoff), err.Error(), exhausted); markErr != nil {
				return fmt.Errorf("记录 outbox 事件 %s 投递失败结果出错: %w", event.EventID, markErr)
			}
			// 保持写入顺序：前一条未确认成功前不再投递后续事件。
			return nil
		}

		if err := r.store.MarkPublished(ctx, event.ID, r.now()); err != nil {
			// 已确认到 broker 但标记失败：下一轮会重复投递，下游按 event_id 幂等。
			return fmt.Errorf("标记 outbox 事件 %s 已发送失败: %w", event.EventID, err)
		}
		log.Printf("outbox 事件已投递: event_id=%s topic=%s exchange=%s routing=%s",
			event.EventID, event.Topic, route.Exchange, route.RoutingKey)
	}
	return nil
}

// retryBackoff 计算指数退避：1s、2s、4s……最长 1 分钟。
func retryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	backoff := time.Second
	for i := 1; i < attempts; i++ {
		backoff *= 2
		if backoff >= maxBackoff {
			return maxBackoff
		}
	}
	return backoff
}
