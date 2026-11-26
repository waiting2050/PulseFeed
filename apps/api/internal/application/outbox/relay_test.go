// relay_test.go 以内存 fake 验证 outbox relay 的投递顺序、confirm 失败退避、上限标记与重复投递。
package outbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

// relayFakeStore 是 outbox 存储的内存替身，记录标记结果并支持错误注入。
type relayFakeStore struct {
	pending          []Event
	publishedIDs     []int64
	publishedAt      []time.Time
	failures         []relayFailure
	listErr          error
	markPublishedErr error
	markFailedErr    error
}

type relayFailure struct {
	id          int64
	attempts    int
	nextAttempt time.Time
	reason      string
	exhausted   bool
}

func (f *relayFakeStore) ListPending(_ context.Context, _ time.Time, limit int) ([]Event, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if limit < len(f.pending) {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}

func (f *relayFakeStore) MarkPublished(_ context.Context, id int64, publishedAt time.Time) error {
	if f.markPublishedErr != nil {
		return f.markPublishedErr
	}
	f.publishedIDs = append(f.publishedIDs, id)
	f.publishedAt = append(f.publishedAt, publishedAt)
	return nil
}

func (f *relayFakeStore) MarkFailed(_ context.Context, id int64, attempts int, nextAttemptAt time.Time, reason string, exhausted bool) error {
	if f.markFailedErr != nil {
		return f.markFailedErr
	}
	f.failures = append(f.failures, relayFailure{
		id:          id,
		attempts:    attempts,
		nextAttempt: nextAttemptAt,
		reason:      reason,
		exhausted:   exhausted,
	})
	return nil
}

// relayFakePublisher 记录每次投递入参并支持失败注入。
type relayFakePublisher struct {
	calls []relayCall
	err   error
}

type relayCall struct {
	exchange  string
	routing   string
	messageID string
	payload   []byte
}

func (p *relayFakePublisher) PublishRaw(_ context.Context, exchange, routing, messageID string, payload []byte) error {
	p.calls = append(p.calls, relayCall{exchange: exchange, routing: routing, messageID: messageID, payload: payload})
	return p.err
}

// relayFixedNow 是测试使用的固定时钟。
var relayFixedNow = time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

// newRelayFixture 构造带两条待投递事件的 relay 与 fake 依赖。
func newRelayFixture() (*Relay, *relayFakeStore, *relayFakePublisher) {
	store := &relayFakeStore{pending: []Event{
		{ID: 1, EventID: "evt-1", Topic: "clip.published", Payload: []byte(`{"clip_id":42}`)},
		{ID: 2, EventID: "evt-2", Topic: "clip.embedding", Payload: []byte(`{"clip_id":42}`)},
	}}
	publisher := &relayFakePublisher{}
	relay := NewRelay(store, publisher, map[string]Route{
		"clip.published": {Exchange: "clip.ex", RoutingKey: "clip.published"},
		"clip.embedding": {Exchange: "clip.ex", RoutingKey: "clip.embedding"},
	})
	relay.now = func() time.Time { return relayFixedNow }
	return relay, store, publisher
}

// TestRelayDrainPublishesInOrderAndMarksSent 验证按写入顺序投递，event_id 写入 MessageId，成功后标记已发送。
func TestRelayDrainPublishesInOrderAndMarksSent(t *testing.T) {
	relay, store, publisher := newRelayFixture()

	if err := relay.Drain(context.Background()); err != nil {
		t.Fatalf("派发不应失败: %v", err)
	}
	if len(publisher.calls) != 2 {
		t.Fatalf("投递次数 = %d, 期望 2", len(publisher.calls))
	}
	if publisher.calls[0].messageID != "evt-1" || publisher.calls[1].messageID != "evt-2" {
		t.Fatalf("投递顺序或 MessageId 不正确: %+v", publisher.calls)
	}
	if publisher.calls[0].exchange != "clip.ex" || publisher.calls[0].routing != "clip.published" {
		t.Fatalf("路由映射不正确: %+v", publisher.calls[0])
	}
	if string(publisher.calls[0].payload) != `{"clip_id":42}` {
		t.Fatalf("载荷被修改: %s", publisher.calls[0].payload)
	}
	if len(store.publishedIDs) != 2 || store.publishedIDs[0] != 1 || store.publishedIDs[1] != 2 {
		t.Fatalf("已发送标记不正确: %v", store.publishedIDs)
	}
	for _, at := range store.publishedAt {
		if !at.Equal(relayFixedNow) {
			t.Fatalf("published_at = %v, 期望 %v", at, relayFixedNow)
		}
	}
	if len(store.failures) != 0 {
		t.Fatalf("成功路径不应记录失败: %+v", store.failures)
	}
}

// TestRelayDrainRetriesWithBackoffOnPublishFailure 验证投递失败保留事件、按指数退避安排下次重试，
// 且前一条失败后不再投递后续事件，保持写入顺序。
func TestRelayDrainRetriesWithBackoffOnPublishFailure(t *testing.T) {
	relay, store, publisher := newRelayFixture()
	publisher.err = errors.New("broker unavailable")

	if err := relay.Drain(context.Background()); err != nil {
		t.Fatalf("记录失败后不应返回错误: %v", err)
	}
	if len(publisher.calls) != 1 {
		t.Fatalf("失败后应停止本轮投递，实际投递 %d 次", len(publisher.calls))
	}
	if len(store.publishedIDs) != 0 {
		t.Fatalf("失败事件不应标记已发送: %v", store.publishedIDs)
	}
	if len(store.failures) != 1 {
		t.Fatalf("失败记录数 = %d, 期望 1", len(store.failures))
	}
	failure := store.failures[0]
	if failure.id != 1 || failure.attempts != 1 || failure.exhausted {
		t.Fatalf("失败记录不正确: %+v", failure)
	}
	if want := relayFixedNow.Add(time.Second); !failure.nextAttempt.Equal(want) {
		t.Fatalf("首次失败退避 = %v, 期望 %v", failure.nextAttempt, want)
	}
	if failure.reason == "" {
		t.Fatalf("失败原因不应为空")
	}
}

// TestRelayDrainMarksFailedAtAttemptLimit 验证达到尝试上限后标记 failed，不再无限重试。
func TestRelayDrainMarksFailedAtAttemptLimit(t *testing.T) {
	relay, store, publisher := newRelayFixture()
	publisher.err = errors.New("broker unavailable")
	store.pending = []Event{{
		ID: 7, EventID: "evt-7", Topic: "clip.published",
		Payload: []byte(`{}`), Attempts: MaxPublishAttempts - 1,
	}}

	if err := relay.Drain(context.Background()); err != nil {
		t.Fatalf("记录失败后不应返回错误: %v", err)
	}
	if len(store.failures) != 1 {
		t.Fatalf("失败记录数 = %d, 期望 1", len(store.failures))
	}
	failure := store.failures[0]
	if failure.attempts != MaxPublishAttempts || !failure.exhausted {
		t.Fatalf("达到上限时标记不正确: %+v", failure)
	}
}

// TestRelayDrainMarksFailedForUnknownTopic 验证未配置路由的事件直接标记 failed，不投递空交换机。
func TestRelayDrainMarksFailedForUnknownTopic(t *testing.T) {
	relay, store, publisher := newRelayFixture()
	store.pending = []Event{{ID: 9, EventID: "evt-9", Topic: "unknown.topic", Payload: []byte(`{}`)}}

	if err := relay.Drain(context.Background()); err != nil {
		t.Fatalf("处理未知 topic 不应返回错误: %v", err)
	}
	if len(publisher.calls) != 0 {
		t.Fatalf("未知 topic 不应投递，实际 %d 次", len(publisher.calls))
	}
	if len(store.failures) != 1 || !store.failures[0].exhausted || store.failures[0].id != 9 {
		t.Fatalf("未知 topic 应标记 failed: %+v", store.failures)
	}
}

// TestRelayDrainRedeliversWhenMarkFails 验证 broker 已确认但标记失败时下一轮会重复投递；
// 两次投递携带同一 event_id，下游可据此幂等，不会产生重复业务效果。
func TestRelayDrainRedeliversWhenMarkFails(t *testing.T) {
	relay, store, publisher := newRelayFixture()
	store.pending = store.pending[:1]
	store.markPublishedErr = errors.New("db unavailable")

	if err := relay.Drain(context.Background()); err == nil {
		t.Fatalf("标记失败应返回错误以便观察")
	}
	store.markPublishedErr = nil
	if err := relay.Drain(context.Background()); err != nil {
		t.Fatalf("重试派发不应失败: %v", err)
	}

	if len(publisher.calls) != 2 {
		t.Fatalf("投递次数 = %d, 期望 2（重复投递）", len(publisher.calls))
	}
	if publisher.calls[0].messageID != publisher.calls[1].messageID || publisher.calls[0].messageID != "evt-1" {
		t.Fatalf("重复投递的 MessageId 不一致: %+v", publisher.calls)
	}
}

// TestRelayRetryBackoff 验证指数退避序列与上限。
func TestRelayRetryBackoff(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: time.Second},
		{attempts: 1, want: time.Second},
		{attempts: 2, want: 2 * time.Second},
		{attempts: 3, want: 4 * time.Second},
		{attempts: 6, want: 32 * time.Second},
		{attempts: 7, want: time.Minute},
		{attempts: 20, want: time.Minute},
	}
	for _, tt := range tests {
		if got := retryBackoff(tt.attempts); got != tt.want {
			t.Fatalf("retryBackoff(%d) = %v, 期望 %v", tt.attempts, got, tt.want)
		}
	}
}
