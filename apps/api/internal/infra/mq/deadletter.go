// deadletter.go 负责任务队列的死信转录与消费：
// 业务消费者在不可重试/重试耗尽时主动把消息转录到死信队列并补诊断头（原队列、原因、重试次数、错误），
// 死信消费者解析这些头，供上层记录事件内容、失败原因与来源队列。
package inframq

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// DeadReasonPermanent 表示不可重试错误导致的死信。
	DeadReasonPermanent = "permanent"
	// DeadReasonExhausted 表示重试次数耗尽导致的死信。
	DeadReasonExhausted = "exhausted"
	// DeadReasonUnknown 表示消息由 broker 直接死信（缺少自定义诊断头）。
	DeadReasonUnknown = "unknown"

	headerOriginalQueue   = "x-pulsefeed-original-queue"
	headerOriginalRouting = "x-pulsefeed-original-routing"
	headerDeadReason      = "x-pulsefeed-dead-reason"
	headerAttempts        = "x-pulsefeed-attempts"
	headerError           = "x-pulsefeed-error"

	// deadLetterRetryDelay 是死信消费失败后重新入队前的等待时间，避免存储故障时高速空转。
	deadLetterRetryDelay = time.Second
	// headerErrorLimit 限制写入诊断头的错误消息长度，避免超出 AMQP 头大小限制。
	headerErrorLimit = 512
)

// DeadLetter 是死信队列中一条消息的解析结果。
type DeadLetter struct {
	// SourceQueue 是消息原本所在的业务队列；无法解析时为 "unknown"。
	SourceQueue string
	// SourceRouting 是消息原本的路由键，便于排查哪类事件被死信。
	SourceRouting string
	// Reason 取值 permanent / exhausted / unknown（或 broker x-death 给出的原因）。
	Reason string
	// Attempts 是本进程内该消息被处理的次数（含首次），用于判断重试了几次。
	Attempts int
	// LastError 是最后一次处理错误，便于直接看出失败原因。
	LastError string
	// MessageID 是 outbox 事件写入的 AMQP MessageId，可直接对应 outbox.event_id。
	MessageID string
	// Payload 是原始消息体，保留完整内容供人工重放。
	Payload []byte
}

// deadRoute 描述业务队列对应的死信交换机与路由键。
type deadRoute struct {
	exchange      string
	routing       string
	sourceRouting string
}

// forwardDeadLetter 把一条无法继续处理的消息转录到死信队列，并写入诊断头。
// 只有 broker 确认接收才返回 nil，调用方据此决定是否 ack 原消息。
func (c *Client) forwardDeadLetter(ctx context.Context, queue string, delivery amqp.Delivery, reason string, attempts int, cause error) error {
	if c == nil {
		return fmt.Errorf("RabbitMQ 未初始化")
	}
	route, ok := c.deadRoutes[queue]
	if !ok {
		return fmt.Errorf("队列 %q 未配置死信路由", queue)
	}
	headers := amqp.Table{
		headerOriginalQueue:   queue,
		headerOriginalRouting: route.sourceRouting,
		headerDeadReason:      reason,
		headerAttempts:        int32(attempts),
	}
	if cause != nil {
		headers[headerError] = truncateHeader(cause.Error())
	}
	return c.publishMessage(ctx, route.exchange, route.routing, delivery.MessageId, headers, delivery.Body)
}

// ConsumeDeadLetters 阻塞消费死信队列：处理成功后 ack；
// 处理失败时重新入队并短暂等待，保证死信消息不会因落库故障被静默丢弃。
func (c *Client) ConsumeDeadLetters(ctx context.Context, queue string, handler func(context.Context, DeadLetter) error) error {
	if c == nil {
		return fmt.Errorf("RabbitMQ 未初始化")
	}

	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("创建死信消费通道失败: %w", err)
	}
	defer ch.Close()

	if err := ch.Qos(consumePrefetch, 0, false); err != nil {
		return fmt.Errorf("设置死信消费预取上限失败: %w", err)
	}

	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("开始消费死信队列 %q 失败: %w", queue, err)
	}

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
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("死信队列 %q 消费通道意外关闭", queue)
			}
			message := ParseDeadLetter(queue, delivery)
			if err := handler(ctx, message); err != nil {
				log.Printf("error: 死信消息处理失败，重新入队避免丢弃 (queue=%q message_id=%s): %v",
					queue, message.MessageID, err)
				if nackErr := delivery.Nack(false, true); nackErr != nil {
					log.Printf("warning: 死信消息重新入队失败 (queue=%q delivery_tag=%d): %v",
						queue, delivery.DeliveryTag, nackErr)
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(deadLetterRetryDelay):
				}
				continue
			}
			if ackErr := delivery.Ack(false); ackErr != nil {
				log.Printf("warning: 死信消息确认失败 (queue=%q delivery_tag=%d): %v",
					queue, delivery.DeliveryTag, ackErr)
			}
		}
	}
}

// ParseDeadLetter 解析死信投递：优先读取本进程写入的自定义诊断头，
// 缺失时回退 broker 的 x-death（旧消费者直接 nack 导致的死信）。
func ParseDeadLetter(deadQueue string, delivery amqp.Delivery) DeadLetter {
	message := DeadLetter{
		MessageID: delivery.MessageId,
		Payload:   delivery.Body,
		Reason:    DeadReasonUnknown,
		Attempts:  1,
	}

	message.SourceQueue = headerString(delivery.Headers, headerOriginalQueue)
	message.SourceRouting = headerString(delivery.Headers, headerOriginalRouting)
	message.LastError = headerString(delivery.Headers, headerError)
	if reason := headerString(delivery.Headers, headerDeadReason); reason != "" {
		message.Reason = reason
	}
	if attempts := headerInt(delivery.Headers, headerAttempts); attempts > 0 {
		message.Attempts = attempts
	}

	// 无自定义头时回退 x-death：broker 记录的原队列、死信次数与原因。
	if death, ok := firstDeath(delivery.Headers); ok {
		if message.SourceQueue == "" {
			message.SourceQueue = death.queue
		}
		if headerInt(delivery.Headers, headerAttempts) <= 0 && death.count > 0 {
			message.Attempts = death.count
		}
		if message.Reason == DeadReasonUnknown && death.reason != "" {
			message.Reason = death.reason
		}
	}
	if message.SourceQueue == "" {
		// 兜底：死信队列命名固定为 <原队列>.dead，去掉后缀即可还原来源队列。
		message.SourceQueue = strings.TrimSuffix(deadQueue, ".dead")
	}
	return message
}

// deathEntry 是 broker x-death 头中的一条死信记录。
type deathEntry struct {
	queue  string
	reason string
	count  int
}

// firstDeath 读取 x-death 的第一条记录；头缺失或结构异常时返回 false。
func firstDeath(headers amqp.Table) (deathEntry, bool) {
	raw, ok := headers["x-death"]
	if !ok {
		return deathEntry{}, false
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return deathEntry{}, false
	}
	table, ok := list[0].(amqp.Table)
	if !ok {
		return deathEntry{}, false
	}
	return deathEntry{
		queue:  headerString(table, "queue"),
		reason: headerString(table, "reason"),
		count:  headerInt(table, "count"),
	}, true
}

// headerString 读取字符串类型头字段，类型不符时返回空串。
func headerString(headers amqp.Table, key string) string {
	if headers == nil {
		return ""
	}
	value, ok := headers[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

// headerInt 读取整型头字段，兼容 AMQP 解析出的多种整型宽度。
func headerInt(headers amqp.Table, key string) int {
	if headers == nil {
		return 0
	}
	switch typed := headers[key].(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

// truncateHeader 按字节上限截断错误信息，避免超长错误撑爆 AMQP 头。
func truncateHeader(text string) string {
	if len(text) <= headerErrorLimit {
		return text
	}
	return text[:headerErrorLimit]
}
