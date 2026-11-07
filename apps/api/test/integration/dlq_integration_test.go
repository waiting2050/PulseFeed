//go:build integration

// dlq_integration_test.go 验证死信拓扑：不可重试错误直接转入死信队列，
// 可重试错误超过重投上限后转入死信队列，且原队列消费后不残留。
// 测试使用带时间戳的独立交换机/队列，结束即删除，不触碰现有拓扑与数据。
package integration

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"pulsefeed/internal/domain/messaging"
	"pulsefeed/internal/infra/config"
	inframq "pulsefeed/internal/infra/mq"
)

const (
	dlqWaitTimeout = 15 * time.Second
	dlqPollStep    = 100 * time.Millisecond
)

// dlqBinding 描述一组业务队列与交换机的绑定关系，供建拓扑与清理复用。
type dlqBinding struct {
	queue    string
	exchange string
	routing  string
}

// newDLQTestConfig 基于唯一后缀构造独立拓扑配置，四个队列全部使用测试前缀。
func newDLQTestConfig(suffix string) (config.RabbitMQConfig, []dlqBinding) {
	clipExchange := "it.dlq." + suffix + ".clip"
	interactionExchange := "it.dlq." + suffix + ".interaction"
	impressionExchange := "it.dlq." + suffix + ".impression"

	cfg := config.RabbitMQConfig{
		URL:                      envOr("PULSEFEED_IT_RABBITMQ_URL", defaultMQURL),
		ClipExchange:             clipExchange,
		ClipPublishedQueue:       clipExchange + ".published",
		ClipPublishedRouting:     "clip.published",
		ClipEmbeddingQueue:       clipExchange + ".embedding",
		InteractionExchange:      interactionExchange,
		ActionChangedQueue:       interactionExchange + ".action_changed",
		ActionChangedRouting:     "interaction.action_changed",
		ImpressionExchange:       impressionExchange,
		PlayEventRecordedQueue:   impressionExchange + ".play_recorded",
		PlayEventRecordedRouting: "impression.play_recorded",
	}
	bindings := []dlqBinding{
		{cfg.ClipPublishedQueue, cfg.ClipExchange, cfg.ClipPublishedRouting},
		{cfg.ClipEmbeddingQueue, cfg.ClipExchange, "clip.embedding"},
		{cfg.ActionChangedQueue, cfg.InteractionExchange, cfg.ActionChangedRouting},
		{cfg.PlayEventRecordedQueue, cfg.ImpressionExchange, cfg.PlayEventRecordedRouting},
	}
	return cfg, bindings
}

// TestDeadLetterOnPermanentError 验证不可重试错误只消费一次即转入死信，原队列不残留。
func TestDeadLetterOnPermanentError(t *testing.T) {
	suffix := fmt.Sprintf("it%x", time.Now().UnixNano())
	cfg, bindings := newDLQTestConfig(suffix)
	runDeadLetterCase(t, cfg, bindings, func(context.Context, []byte) error {
		return messaging.Permanent(errors.New("模拟不可重试的业务错误"))
	}, 1)
}

// TestDeadLetterAfterRetryExhausted 验证可重试错误重投到上限后才转入死信，原队列不残留。
func TestDeadLetterAfterRetryExhausted(t *testing.T) {
	suffix := fmt.Sprintf("it%x", time.Now().UnixNano())
	cfg, bindings := newDLQTestConfig(suffix)
	runDeadLetterCase(t, cfg, bindings, func(context.Context, []byte) error {
		return errors.New("模拟持续瞬时失败")
	}, inframq.MaxDeliveryAttempts)
}

// runDeadLetterCase 投递一条必失败消息，等待其进入死信队列并核对消费次数与原队列深度。
func runDeadLetterCase(t *testing.T, cfg config.RabbitMQConfig, bindings []dlqBinding,
	handler func(context.Context, []byte) error, wantHandled int32) {
	t.Helper()

	client, err := inframq.New(cfg)
	if err != nil {
		t.Fatalf("初始化 RabbitMQ 客户端失败: %v", err)
	}
	defer client.Close()
	t.Cleanup(func() { cleanupDLQTopology(t, cfg, bindings) })

	conn := dialAMQP(t, cfg.URL)
	defer conn.close()

	_, deadQueue, _ := inframq.DeadLetterNames(cfg.ClipExchange, cfg.ClipPublishedQueue, cfg.ClipPublishedRouting)
	payload := []byte("not-json-poison-payload")
	conn.publish(t, cfg.ClipExchange, cfg.ClipPublishedRouting, payload)

	var handled atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := client.Consume(ctx, cfg.ClipPublishedQueue, func(ctx context.Context, body []byte) error {
			handled.Add(1)
			return handler(ctx, body)
		}); err != nil {
			t.Logf("消费者退出: %v", err)
		}
	}()

	conn.waitDepth(t, deadQueue, 1, dlqWaitTimeout)
	cancel()
	<-done

	if got := handled.Load(); got != wantHandled {
		t.Fatalf("原队列消费次数 = %d, 期望 %d（%d 次后应转入死信不再重投）",
			got, wantHandled, inframq.MaxDeliveryAttempts)
	}
	if depth := conn.depth(t, cfg.ClipPublishedQueue); depth != 0 {
		t.Fatalf("原队列残留 %d 条消息, 期望 0", depth)
	}

	deathBody := conn.readOne(t, deadQueue)
	if string(deathBody) != string(payload) {
		t.Fatalf("死信载荷 = %q, 期望 %q", deathBody, payload)
	}
	if depth := conn.depth(t, deadQueue); depth != 0 {
		t.Fatalf("读取后死信队列残留 %d 条消息, 期望 0", depth)
	}
}

// amqpTestConn 封装集成测试复用的连接与通道，避免轮询时反复建连。
type amqpTestConn struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// dialAMQP 建立测试专用 AMQP 连接与通道。
func dialAMQP(t *testing.T, url string) *amqpTestConn {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("连接 RabbitMQ 失败（%s）: %v", url, err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		t.Fatalf("创建 AMQP 通道失败: %v", err)
	}
	return &amqpTestConn{conn: conn, ch: ch}
}

// close 关闭测试通道与连接。
func (c *amqpTestConn) close() {
	_ = c.ch.Close()
	_ = c.conn.Close()
}

// publish 向指定交换机投递一条持久化消息。
func (c *amqpTestConn) publish(t *testing.T, exchange, routing string, body []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ch.PublishWithContext(ctx, exchange, routing, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	}); err != nil {
		t.Fatalf("投递消息到 %s(%s) 失败: %v", exchange, routing, err)
	}
}

// depth 读取队列当前消息数。
func (c *amqpTestConn) depth(t *testing.T, queue string) int {
	t.Helper()
	q, err := c.ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		t.Fatalf("读取队列 %q 深度失败: %v", queue, err)
	}
	return q.Messages
}

// waitDepth 轮询等待队列消息数达到期望值。
func (c *amqpTestConn) waitDepth(t *testing.T, queue string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := c.depth(t, queue); got >= want {
			return
		}
		time.Sleep(dlqPollStep)
	}
	t.Fatalf("等待队列 %q 达到 %d 条消息超时（当前 %d 条）", queue, want, c.depth(t, queue))
}

// readOne 自动确认读取一条死信消息，返回消息体。
func (c *amqpTestConn) readOne(t *testing.T, queue string) []byte {
	t.Helper()
	deliveries, err := c.ch.Consume(queue, "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("消费死信队列 %q 失败: %v", queue, err)
	}
	select {
	case delivery := <-deliveries:
		return delivery.Body
	case <-time.After(dlqWaitTimeout):
		t.Fatalf("等待死信队列 %q 消息超时", queue)
		return nil
	}
}

// cleanupDLQTopology 删除本次测试创建的死信与业务队列/交换机。
func cleanupDLQTopology(t *testing.T, cfg config.RabbitMQConfig, bindings []dlqBinding) {
	t.Helper()
	conn, err := amqp.Dial(cfg.URL)
	if err != nil {
		t.Logf("清理死信拓扑时连接失败，跳过清理: %v", err)
		return
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Logf("清理死信拓扑时创建通道失败，跳过清理: %v", err)
		return
	}
	defer ch.Close()

	for _, b := range bindings {
		dlx, deadQueue, _ := inframq.DeadLetterNames(b.exchange, b.queue, b.routing)
		_, _ = ch.QueueDelete(deadQueue, false, false, false)
		_, _ = ch.QueueDelete(b.queue, false, false, false)
		_ = ch.ExchangeDelete(dlx, false, false)
	}
	_ = ch.ExchangeDelete(cfg.ClipExchange, false, false)
	_ = ch.ExchangeDelete(cfg.InteractionExchange, false, false)
	_ = ch.ExchangeDelete(cfg.ImpressionExchange, false, false)
}
