// consumer.go 实现死信队列消费者：把死信消息的事件内容、来源队列、失败原因与重试次数
// 落结构化日志与存储，并上报指标；落库失败时返回错误，由 MQ 重新入队，绝不静默丢弃。
package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

const (
	// EventTypeUnknown 是无法从来源队列或载荷识别事件类型时的兜底标签。
	EventTypeUnknown = "unknown"
	// ReasonUnknown 是缺少失败原因时的兜底标签。
	ReasonUnknown = "unknown"
	// SourceQueueUnknown 是来源队列缺失时的兜底标签。
	SourceQueueUnknown = "unknown"
	// logPayloadLimit 限制单条死信日志中的载荷长度，完整内容由存储保留。
	logPayloadLimit = 2048
)

// Message 是待记录的一条死信消息。
type Message struct {
	// SourceQueue 是消息原本所在的业务队列。
	SourceQueue string
	// EventType 是逻辑事件类型（如 clip.published），由来源队列映射得到。
	EventType string
	// EventID 是业务事件 ID，可从载荷中的 event_id 解析。
	EventID string
	// Reason 是死信原因：permanent / exhausted / unknown。
	Reason string
	// Attempts 是消费进程内的处理次数（含首次），用于判断重试了几次。
	Attempts int
	// LastError 是最后一次处理失败的错误信息。
	LastError string
	// MessageID 是 AMQP MessageId，可直接对应 outbox 的 event_id。
	MessageID string
	// Payload 是原始消息体。
	Payload []byte
	// ReceivedAt 是死信被消费的时间，由消费者填充。
	ReceivedAt time.Time
}

// Store 是死信消息的持久化端口，由 infra/persistence/dlq 实现。
type Store interface {
	// Save 记录一条死信消息；同一消息重复消费时应按去重键幂等写入。
	Save(ctx context.Context, message Message) error
}

// Observer 是死信指标端口，由 infra/metrics 实现。
type Observer interface {
	// ObserveDeadLetter 记录一条死信，按来源队列、事件类型与原因区分。
	ObserveDeadLetter(queue, eventType, reason string)
}

// Consumer 负责把死信消息落库、记日志并上报指标。
type Consumer struct {
	store      Store
	observer   Observer
	eventTypes map[string]string
	now        func() time.Time
}

// New 创建死信消费者；eventTypes 是「业务队列 → 逻辑事件类型」的映射。
func New(store Store, observer Observer, eventTypes map[string]string) *Consumer {
	return &Consumer{
		store:      store,
		observer:   observer,
		eventTypes: eventTypes,
		now:        time.Now,
	}
}

// Handle 处理一条死信消息：补全事件类型与事件 ID，先落库再记日志与指标。
// 落库失败时返回错误，消息由 MQ 重新入队，避免“消费后静默丢弃”。
func (c *Consumer) Handle(ctx context.Context, message Message) error {
	if message.SourceQueue == "" {
		message.SourceQueue = SourceQueueUnknown
	}
	if message.Reason == "" {
		message.Reason = ReasonUnknown
	}
	if message.EventType == "" {
		message.EventType = c.eventTypes[message.SourceQueue]
	}
	if message.EventType == "" {
		message.EventType = EventTypeUnknown
	}
	if message.EventID == "" {
		message.EventID = eventIDFromPayload(message.Payload)
	}
	if message.ReceivedAt.IsZero() {
		message.ReceivedAt = c.now()
	}

	if err := c.store.Save(ctx, message); err != nil {
		log.Printf("error: 死信消息落库失败，保留在死信队列待重试 (source_queue=%s event_type=%s event_id=%s message_id=%s): %v",
			message.SourceQueue, message.EventType, message.EventID, message.MessageID, err)
		return fmt.Errorf("记录死信消息失败: %w", err)
	}

	// 结构化日志：一眼看出哪类事件、来自哪个原队列、重试了几次、失败原因与事件内容。
	log.Printf("warning: 死信消息 source_queue=%s event_type=%s event_id=%s message_id=%s reason=%s attempts=%d error=%q payload=%s",
		message.SourceQueue, message.EventType, message.EventID, message.MessageID,
		message.Reason, message.Attempts, message.LastError, truncatePayload(message.Payload))

	if c.observer != nil {
		c.observer.ObserveDeadLetter(message.SourceQueue, message.EventType, message.Reason)
	}
	return nil
}

// eventIDFromPayload 从消息体中解析 event_id；载荷非法或缺失时返回空串。
func eventIDFromPayload(payload []byte) string {
	var probe struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return ""
	}
	return probe.EventID
}

// truncatePayload 截断日志中的载荷，完整内容仍由存储保留。
func truncatePayload(payload []byte) string {
	if len(payload) <= logPayloadLimit {
		return string(payload)
	}
	return string(payload[:logPayloadLimit]) + "...(truncated)"
}
