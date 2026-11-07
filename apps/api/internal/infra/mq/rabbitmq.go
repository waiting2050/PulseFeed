// rabbitmq.go 封装 RabbitMQ 连接、三组交换机/队列声明、事件发布与手动 ack 的消费循环。
package inframq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sync"
	"time"

	appclip "pulsefeed/internal/application/clip"
	appimpression "pulsefeed/internal/application/impression"
	appaction "pulsefeed/internal/application/interaction"
	"pulsefeed/internal/domain/messaging"
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
	// MaxDeliveryAttempts 是单条消息可重试次数上限；达到上限仍失败则转入死信队列。
	MaxDeliveryAttempts = 3
)

// DeadLetterNames 返回死信交换机、死信队列与死信路由键，命名由业务交换机与队列推导。
func DeadLetterNames(exchange, queue, routing string) (dlx, deadQueue, deadRouting string) {
	return exchange + ".dlx", queue + ".dead", "dead." + routing
}

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

	// 死信交换机需先于队列声明存在，否则队列参数 x-dead-letter-exchange 无法生效。
	declaredDLX := make(map[string]struct{}, len(bindings))
	for _, b := range bindings {
		dlx, _, _ := DeadLetterNames(b.exchange, b.queue, b.routing)
		if _, ok := declaredDLX[dlx]; ok {
			continue
		}
		if err := ch.ExchangeDeclare(dlx, "topic", true, false, false, false, nil); err != nil {
			return fmt.Errorf("声明死信交换机 %q 失败: %w", dlx, err)
		}
		declaredDLX[dlx] = struct{}{}
	}

	for _, b := range bindings {
		dlx, deadQueue, deadRouting := DeadLetterNames(b.exchange, b.queue, b.routing)
		args := amqp.Table{
			"x-dead-letter-exchange":    dlx,
			"x-dead-letter-routing-key": deadRouting,
		}
		if err := declareQueue(ch, b.queue, b.exchange, b.routing, args); err != nil {
			return err
		}
		// 死信队列自身不再配置死信参数，避免循环死信。
		if err := declareQueue(ch, deadQueue, dlx, deadRouting, nil); err != nil {
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

// declareQueue 声明持久化队列（可带死信参数）并绑定到指定交换机与路由键。
func declareQueue(ch *amqp.Channel, queue, exchange, routing string, args amqp.Table) error {
	if _, err := ch.QueueDeclare(queue, true, false, false, false, args); err != nil {
		// 同名队列参数不一致时 broker 返回 406；新增死信参数后旧队列需显式删除才能重建。
		var amqpErr *amqp.Error
		if errors.As(err, &amqpErr) && amqpErr.Code == amqp.PreconditionFailed {
			return fmt.Errorf("队列 %q 已存在且参数不一致（新增死信参数），请删除旧队列后重启（rabbitmqctl delete_queue %s）: %w",
				queue, queue, err)
		}
		return fmt.Errorf("声明队列 %q 失败: %w", queue, err)
	}
	if err := ch.QueueBind(queue, routing, exchange, false, nil); err != nil {
		return fmt.Errorf("绑定队列 %q 到 %q(%s) 失败: %w", queue, exchange, routing, err)
	}
	return nil
}

// PublishClipPublished 投递 clip.published 事件，供扇出消费者使用。
func (c *Client) PublishClipPublished(ctx context.Context, event appclip.ClipPublished) error {
	return c.publish(ctx, c.cfg.ClipExchange, c.cfg.ClipPublishedRouting, event)
}

// PublishClipEmbedding 投递 clip.embedding 事件，由 ClipEmbeddingWorker 生成文本向量。
func (c *Client) PublishClipEmbedding(ctx context.Context, event appclip.ClipPublished) error {
	return c.publish(ctx, c.cfg.ClipExchange, embeddingRouting, event)
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

	// attempts 记录本消费循环内每条消息的失败次数；重投消息 body 不变，按内容哈希累计。
	attempts := make(map[uint64]int)

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
				key := messageKey(delivery.Body)
				attempts[key]++
				permanent := messaging.IsPermanent(err)
				exhausted := attempts[key] >= MaxDeliveryAttempts
				if permanent || exhausted {
					reason := "不可重试"
					if !permanent {
						reason = fmt.Sprintf("超过重试上限 %d 次", MaxDeliveryAttempts)
					}
					// 不重新入队：broker 按队列的 x-dead-letter-exchange 转入死信队列。
					log.Printf("warning: 消费队列 %q 消息失败，转入死信队列（%s, delivery_tag=%d）: %v",
						queue, reason, delivery.DeliveryTag, err)
					if nackErr := delivery.Nack(false, false); nackErr != nil {
						log.Printf("warning: 消息转入死信失败 (delivery_tag=%d): %v", delivery.DeliveryTag, nackErr)
					}
					delete(attempts, key)
					continue
				}
				// 可重试错误重新入队，超过 MaxDeliveryAttempts 后由上面的分支转入死信。
				log.Printf("warning: 消费队列 %q 消息失败，重试 %d/%d 次 (delivery_tag=%d): %v",
					queue, attempts[key], MaxDeliveryAttempts, delivery.DeliveryTag, err)
				if nackErr := delivery.Nack(false, true); nackErr != nil {
					log.Printf("warning: 消息重新入队失败 (delivery_tag=%d): %v", delivery.DeliveryTag, nackErr)
				}
				continue
			}
			delete(attempts, messageKey(delivery.Body))
			if err := delivery.Ack(false); err != nil {
				log.Printf("warning: 消息确认失败 (delivery_tag=%d): %v", delivery.DeliveryTag, err)
			}
		}
	}
}

// messageKey 用消息内容哈希标识一次投递，重投后内容不变因而可累计失败次数。
func messageKey(body []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(body)
	return h.Sum64()
}
