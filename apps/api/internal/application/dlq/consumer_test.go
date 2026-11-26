// consumer_test.go 用内存 fake 验证死信消费者的归类、落库、指标上报与失败不吞错契约。
package dlq

import (
	"context"
	"errors"
	"testing"
)

// fakeStore 记录死信消息并支持错误注入。
type fakeStore struct {
	saved []Message
	err   error
}

// Save 记录消息或返回注入的错误。
func (f *fakeStore) Save(_ context.Context, message Message) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, message)
	return nil
}

// fakeObserver 记录指标调用。
type fakeObserver struct {
	queues     []string
	eventTypes []string
	reasons    []string
}

// ObserveDeadLetter 追加一次指标记录。
func (o *fakeObserver) ObserveDeadLetter(queue, eventType, reason string) {
	o.queues = append(o.queues, queue)
	o.eventTypes = append(o.eventTypes, eventType)
	o.reasons = append(o.reasons, reason)
}

// TestHandleRecordsSourceQueueEventTypeAndAttempts 验证消费者补齐来源队列、事件类型与事件 ID，
// 并把完整消息落库后上报指标。
func TestHandleRecordsSourceQueueEventTypeAndAttempts(t *testing.T) {
	store := &fakeStore{}
	observer := &fakeObserver{}
	consumer := New(store, observer, map[string]string{
		"pulsefeed.clip.published": "clip.published",
	})

	err := consumer.Handle(context.Background(), Message{
		SourceQueue: "pulsefeed.clip.published",
		Reason:      "exhausted",
		Attempts:    3,
		LastError:   "数据库不可用",
		MessageID:   "evt-1",
		Payload:     []byte(`{"event_id":"evt-1","clip_id":10}`),
	})
	if err != nil {
		t.Fatalf("处理死信返回错误: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("落库次数 = %d, 期望 1", len(store.saved))
	}
	saved := store.saved[0]
	if saved.EventType != "clip.published" {
		t.Fatalf("事件类型 = %s, 期望 clip.published", saved.EventType)
	}
	if saved.EventID != "evt-1" {
		t.Fatalf("事件 ID = %s, 期望从载荷解析出 evt-1", saved.EventID)
	}
	if saved.Attempts != 3 || saved.Reason != "exhausted" || saved.LastError != "数据库不可用" {
		t.Fatalf("失败原因/重试次数丢失: %+v", saved)
	}
	if saved.ReceivedAt.IsZero() {
		t.Fatalf("缺少死信接收时间")
	}
	if len(observer.queues) != 1 || observer.eventTypes[0] != "clip.published" || observer.reasons[0] != "exhausted" {
		t.Fatalf("指标标签 = %v/%v/%v, 期望来源队列/事件类型/原因各一次",
			observer.queues, observer.eventTypes, observer.reasons)
	}
}

// TestHandleFallsBackToUnknownLabels 验证来源队列无法映射时使用 unknown 兜底标签，仍然落库。
func TestHandleFallsBackToUnknownLabels(t *testing.T) {
	store := &fakeStore{}
	observer := &fakeObserver{}
	consumer := New(store, observer, nil)

	if err := consumer.Handle(context.Background(), Message{
		SourceQueue: "pulsefeed.unknown.queue",
		Payload:     []byte("not-json"),
	}); err != nil {
		t.Fatalf("处理死信返回错误: %v", err)
	}
	saved := store.saved[0]
	if saved.EventType != EventTypeUnknown || saved.Reason != ReasonUnknown {
		t.Fatalf("兜底标签 = %s/%s, 期望 unknown/unknown", saved.EventType, saved.Reason)
	}
	if saved.EventID != "" {
		t.Fatalf("非法载荷不应解析出事件 ID, 实际 %q", saved.EventID)
	}
}

// TestHandleReturnsErrorWhenStoreFails 验证落库失败时返回错误（消息重新入队），不静默丢弃。
func TestHandleReturnsErrorWhenStoreFails(t *testing.T) {
	store := &fakeStore{err: errors.New("写入失败")}
	observer := &fakeObserver{}
	consumer := New(store, observer, nil)

	err := consumer.Handle(context.Background(), Message{
		SourceQueue: "pulsefeed.clip.published",
		Reason:      "permanent",
		Payload:     []byte(`{"event_id":"evt-1"}`),
	})
	if err == nil {
		t.Fatalf("落库失败应返回错误，避免消息被 ack 后丢弃")
	}
	if !errors.Is(err, store.err) {
		t.Fatalf("错误 = %v, 期望包裹 %v", err, store.err)
	}
	if len(observer.queues) != 0 {
		t.Fatalf("落库失败不应上报成功指标, 实际 %d 次", len(observer.queues))
	}
}
