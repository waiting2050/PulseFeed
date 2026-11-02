// rabbitmq.go 封装 RabbitMQ 连接、三组交换机/队列声明、事件发布与手动 ack 的消费循环。
package inframq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	appclip "pulsefeed/internal/application/clip"
	appimpression "pulsefeed/internal/application/impression"
	appaction "pulsefeed/internal/application/interaction"
	"pulsefeed/internal/infra/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// dialTimeout 限制建立 AMQP 连接的最长时长，避免服务启动被不可达的 broker 长时间阻塞。
	dialTimeout = 5 * time.Second
	// publishTimeout 限制单次发布的等待时长。
	publishTimeout = 5 * time.Second
	// consumePrefetch 限制单个消费者未确认消息的上限，防止一次性拉取过多消息。
	consumePrefetch = 16
	// embeddingRouting 是向量生成队列的路由键；配置项仅给出队列名，路由键按命名形态补全。
	embeddingRouting = "clip.embedding"
)

// Client 是 RabbitMQ 客户端，持有连接与一条串行化使用的发布通道。
type Client struct {
	cfg  config.RabbitMQConfig
	conn *amqp.Connection

	// publishMu 保护 publishCh：amqp 通道不是并发安全的，发布必须串行化。
	publishMu sync.Mutex
	publishCh *amqp.Channel
}

// New 建立连接并声明三组交换机/队列/绑定；url 为空时返回 nil 表示 MQ 链路禁用。
func New(cfg config.RabbitMQConfig) (*Client, error) {
	if cfg.URL == "" {
		return nil, nil
	}

	conn, err := amqp.DialConfig(cfg.URL, amqp.Config{Dial: amqp.DefaultDial(dialTimeout)})
	if err != nil {
		return nil, fmt.Errorf("连接 RabbitMQ 失败: %w", err)
	}

	client := &Client{cfg: cfg, conn: conn}
	if err := client.declareTopology(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

// Close 关闭发布通道与连接；nil 客户端安全返回。
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	if c.publishCh != nil {
		_ = c.publishCh.Close()
	}
	_ = c.conn.Close()
}

// declareTopology 声明三个交换机、各自的队列与绑定关系。
func (c *Client) declareTopology() error {
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("创建 RabbitMQ 声明通道失败: %w", err)
	}
	defer ch.Close()

	exchanges := []string{
		c.cfg.ClipExchange,
		c.cfg.InteractionExchange,
		c.cfg.ImpressionExchange,
	}
	for _, name := range exchanges {
		if err := ch.ExchangeDeclare(name, "topic", true, false, false, false, nil); err != nil {
			return fmt.Errorf("声明交换机 %q 失败: %w", name, err)
		}
	}

	bindings := []struct {
		queue    string
		exchange string
		routing  string
	}{
		{c.cfg.ClipPublishedQueue, c.cfg.ClipExchange, c.cfg.ClipPublishedRouting},
		{c.cfg.ClipEmbeddingQueue, c.cfg.ClipExchange, embeddingRouting},
		{c.cfg.ActionChangedQueue, c.cfg.InteractionExchange, c.cfg.ActionChangedRouting},
		{c.cfg.PlayEventRecordedQueue, c.cfg.ImpressionExchange, c.cfg.PlayEventRecordedRouting},
	}
	for _, b := range bindings {
		if err := declareQueue(ch, b.queue, b.exchange, b.routing); err != nil {
			return err
		}
	}

	// 发布通道在拓扑声明完成后保持打开，供后续 Publish* 串行复用。
	publishCh, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("创建 RabbitMQ 发布通道失败: %w", err)
	}
	c.publishCh = publishCh
	return nil
}

// declareQueue 声明持久化队列并绑定到指定交换机与路由键。
func declareQueue(ch *amqp.Channel, queue, exchange, routing string) error {
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明队列 %q 失败: %w", queue, err)
	}
	if err := ch.QueueBind(queue, routing, exchange, false, nil); err != nil {
		return fmt.Errorf("绑定队列 %q 到 %q(%s) 失败: %w", queue, exchange, routing, err)
	}
	return nil
}

// PublishClipPublished 投递 clip.published 事件，供后续阶段扇出/向量消费者使用。
func (c *Client) PublishClipPublished(ctx context.Context, event appclip.ClipPublished) error {
	return c.publish(ctx, c.cfg.ClipExchange, c.cfg.ClipPublishedRouting, event)
}

// PublishActionChanged 投递 interaction.action_changed 事件，由 ActionWorker 落库。
func (c *Client) PublishActionChanged(ctx context.Context, event appaction.ActionChanged) error {
	return c.publish(ctx, c.cfg.InteractionExchange, c.cfg.ActionChangedRouting, event)
}

// PublishPlayRecorded 投递 impression.play_recorded 事件，由 PlayEventWorker 消费做曝光补偿。
func (c *Client) PublishPlayRecorded(ctx context.Context, event appimpression.PlayRecorded) error {
	return c.publish(ctx, c.cfg.ImpressionExchange, c.cfg.PlayEventRecordedRouting, event)
}

// publish 将载荷序列化为 JSON 并以持久化消息投递；调用方需保证事件已落库或可重放。
func (c *Client) publish(ctx context.Context, exchange, routing string, payload any) error {
	if c == nil {
		return fmt.Errorf("RabbitMQ 未初始化")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化事件失败: %w", err)
	}

	// 单次发布超时：broker 异常时不无限等待，便于上层回退同步链路。
	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	if c.publishCh == nil || c.publishCh.IsClosed() {
		return fmt.Errorf("RabbitMQ 发布通道已关闭")
	}
	err = c.publishCh.PublishWithContext(publishCtx, exchange, routing, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Body:         body,
	})
	if err != nil {
		return fmt.Errorf("投递事件到 %s(%s) 失败: %w", exchange, routing, err)
	}
	return nil
}

// Consume 阻塞消费指定队列，逐条回调 handler 并手动 ack/nack；
// ctx 取消后关闭消费通道并返回，实现优雅退出。
func (c *Client) Consume(ctx context.Context, queue string, handler func(context.Context, []byte) error) error {
	if c == nil {
		return fmt.Errorf("RabbitMQ 未初始化")
	}

	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("创建消费通道失败: %w", err)
	}
	defer ch.Close()

	if err := ch.Qos(consumePrefetch, 0, false); err != nil {
		return fmt.Errorf("设置消费预取上限失败: %w", err)
	}

	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("开始消费队列 %q 失败: %w", queue, err)
	}

	// ctx 取消时关闭通道，让 deliveries 通道关闭从而退出阻塞循环。
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ch.Close()
		case <-done:
		}
	}()
	defer close(done)

	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				// ctx 未取消却读不到投递：连接被 broker 或网络中断，交给主入口记录并退出。
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("队列 %q 消费通道意外关闭", queue)
			}
			if err := handler(ctx, delivery.Body); err != nil {
				// 处理失败重新入队，由下游按领域错误判定是否可终止，避免消息静默丢失。
				log.Printf("warning: 消费队列 %q 消息失败 (delivery_tag=%d): %v", queue, delivery.DeliveryTag, err)
				if nackErr := delivery.Nack(false, true); nackErr != nil {
					log.Printf("warning: 消息重新入队失败 (delivery_tag=%d): %v", delivery.DeliveryTag, nackErr)
				}
				continue
			}
			if err := delivery.Ack(false); err != nil {
				log.Printf("warning: 消息确认失败 (delivery_tag=%d): %v", delivery.DeliveryTag, err)
			}
		}
	}
}
