//go:build integration

// outbox_integration_test.go 验证事务性 outbox 的端到端链路：
// clip 与两条事件同事务落库 → relay 经 publisher confirm 投递并标记已发送 →
// 模拟“已投递但标记前崩溃”后重投，消息携带同一 event_id，下游可幂等去重。
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	appclip "pulsefeed/internal/application/clip"
	appoutbox "pulsefeed/internal/application/outbox"
	inframq "pulsefeed/internal/infra/mq"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraoutbox "pulsefeed/internal/infra/persistence/outbox"
)

func TestOutboxRelayPublishesClipEvents(t *testing.T) {
	suffix := fmt.Sprintf("it%x", time.Now().UnixNano())
	cfg, bindings := newDLQTestConfig(suffix)

	client, err := inframq.New(cfg)
	if err != nil {
		t.Fatalf("初始化 RabbitMQ 客户端失败: %v", err)
	}
	defer client.Close()
	t.Cleanup(func() { cleanupDLQTopology(t, cfg, bindings) })

	ctx := context.Background()
	clip, err := appclip.New(infraclip.New(testDB), appclip.WithOutbox()).
		CreatePublished(ctx, 9001, "outbox 集成测试", "", "https://cdn.example.com/it.mp4", "", 0, uniqueKey("outbox-clip"))
	if err != nil {
		t.Fatalf("发布 clip 失败: %v", err)
	}
	t.Cleanup(func() {
		testDB.Delete(&infraclip.ClipModel{}, clip.ID)
		testDB.Delete(&infraclip.ClipStatsModel{}, clip.ID)
	})

	events := outboxEventsForClip(t, clip.ID)
	if len(events) != 2 {
		t.Fatalf("clip 对应 outbox 事件数 = %d, 期望 2", len(events))
	}
	if events[0].Topic != appclip.TopicClipPublished || events[1].Topic != appclip.TopicClipEmbedding {
		t.Fatalf("事件 topic 顺序不正确: %q, %q", events[0].Topic, events[1].Topic)
	}
	if events[0].EventID == "" || events[0].EventID == events[1].EventID {
		t.Fatalf("事件 event_id 为空或重复: %q, %q", events[0].EventID, events[1].EventID)
	}

	relay := appoutbox.NewRelay(infraoutbox.New(testDB), client, map[string]appoutbox.Route{
		appclip.TopicClipPublished: {Exchange: cfg.ClipExchange, RoutingKey: cfg.ClipPublishedRouting},
		appclip.TopicClipEmbedding: {Exchange: cfg.ClipExchange, RoutingKey: inframq.EmbeddingRouting},
	})
	if err := relay.Drain(ctx); err != nil {
		t.Fatalf("派发 outbox 事件失败: %v", err)
	}

	conn := dialAMQP(t, cfg.URL)
	defer conn.close()
	delivery := consumeOne(t, conn, cfg.ClipPublishedQueue)
	if delivery.MessageId != events[0].EventID {
		t.Fatalf("MessageId = %q, 期望 event_id %q", delivery.MessageId, events[0].EventID)
	}
	var payload appclip.ClipPublished
	if err := json.Unmarshal(delivery.Body, &payload); err != nil {
		t.Fatalf("消息载荷无法解析: %v", err)
	}
	if payload.ClipID != clip.ID || payload.EventID != events[0].EventID {
		t.Fatalf("消息载荷不正确: %+v", payload)
	}

	var sent infraoutbox.EventModel
	if err := testDB.First(&sent, events[0].ID).Error; err != nil {
		t.Fatalf("读取已投递事件失败: %v", err)
	}
	if sent.Status != infraoutbox.StatusPublished || sent.PublishedAt == nil {
		t.Fatalf("事件未标记已发送: status=%s published_at=%v", sent.Status, sent.PublishedAt)
	}

	// 模拟“broker 已确认但进程在标记前崩溃”：重置为 pending 后重投一次。
	if err := testDB.Model(&infraoutbox.EventModel{}).
		Where("id = ?", events[0].ID).
		Updates(map[string]any{
			"status":          infraoutbox.StatusPending,
			"published_at":    nil,
			"next_attempt_at": time.Now().Add(-time.Second),
		}).Error; err != nil {
		t.Fatalf("重置事件状态失败: %v", err)
	}
	if err := relay.Drain(ctx); err != nil {
		t.Fatalf("重投 outbox 事件失败: %v", err)
	}

	redelivered := consumeOne(t, conn, cfg.ClipPublishedQueue)
	if redelivered.MessageId != delivery.MessageId || string(redelivered.Body) != string(delivery.Body) {
		t.Fatalf("重复投递的消息应与首次一致: message_id=%q body=%s", redelivered.MessageId, redelivered.Body)
	}
}

// outboxEventsForClip 读取载荷中 clip_id 匹配的 outbox 事件，忽略共享 schema 中的其他数据。
func outboxEventsForClip(t *testing.T, clipID int64) []infraoutbox.EventModel {
	t.Helper()
	var all []infraoutbox.EventModel
	if err := testDB.Order("id ASC").Find(&all).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	matched := make([]infraoutbox.EventModel, 0, 2)
	for _, event := range all {
		var payload appclip.ClipPublished
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if payload.ClipID == clipID {
			matched = append(matched, event)
		}
	}
	return matched
}

// consumeOne 从指定队列自动确认读取一条消息，超时则终止测试。
// 使用 Get 而不是 Consume：同一通道上多次注册消费者会轮询分发，导致后续读取错过消息。
func consumeOne(t *testing.T, conn *amqpTestConn, queue string) amqp.Delivery {
	t.Helper()
	deadline := time.Now().Add(dlqWaitTimeout)
	for time.Now().Before(deadline) {
		delivery, ok, err := conn.ch.Get(queue, true)
		if err != nil {
			t.Fatalf("读取队列 %q 消息失败: %v", queue, err)
		}
		if ok {
			return delivery
		}
		time.Sleep(dlqPollStep)
	}
	t.Fatalf("等待队列 %q 消息超时", queue)
	return amqp.Delivery{}
}
