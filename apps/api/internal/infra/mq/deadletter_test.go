// deadletter_test.go 验证死信诊断头的解析与 broker x-death 回退。
package inframq

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TestParseDeadLetterUsesCustomHeaders 验证优先使用本进程写入的诊断头。
func TestParseDeadLetterUsesCustomHeaders(t *testing.T) {
	delivery := amqp.Delivery{
		MessageId: "evt-1",
		Body:      []byte(`{"event_id":"evt-1"}`),
		Headers: amqp.Table{
			headerOriginalQueue:   "pulsefeed.impression.play_recorded",
			headerOriginalRouting: "impression.play_recorded",
			headerDeadReason:      DeadReasonExhausted,
			headerAttempts:        int32(3),
			headerError:           "数据库不可用",
		},
	}

	message := ParseDeadLetter("pulsefeed.impression.play_recorded.dead", delivery)
	if message.SourceQueue != "pulsefeed.impression.play_recorded" {
		t.Fatalf("来源队列 = %s, 期望原队列", message.SourceQueue)
	}
	if message.SourceRouting != "impression.play_recorded" {
		t.Fatalf("来源路由 = %s, 期望 impression.play_recorded", message.SourceRouting)
	}
	if message.Reason != DeadReasonExhausted || message.Attempts != 3 {
		t.Fatalf("原因/次数 = %s/%d, 期望 exhausted/3", message.Reason, message.Attempts)
	}
	if message.LastError != "数据库不可用" {
		t.Fatalf("失败原因 = %q, 期望数据库不可用", message.LastError)
	}
	if message.MessageID != "evt-1" || string(message.Payload) != `{"event_id":"evt-1"}` {
		t.Fatalf("消息 ID/载荷丢失: %+v", message)
	}
}

// TestParseDeadLetterFallsBackToXDeath 验证缺少自定义头时回退 broker 的 x-death。
func TestParseDeadLetterFallsBackToXDeath(t *testing.T) {
	delivery := amqp.Delivery{
		Body: []byte(`{}`),
		Headers: amqp.Table{
			"x-death": []any{
				amqp.Table{
					"queue":  "pulsefeed.clip.published",
					"reason": "rejected",
					"count":  int64(3),
				},
			},
		},
	}

	message := ParseDeadLetter("pulsefeed.clip.published.dead", delivery)
	if message.SourceQueue != "pulsefeed.clip.published" {
		t.Fatalf("来源队列 = %s, 期望从 x-death 解析", message.SourceQueue)
	}
	if message.Reason != "rejected" || message.Attempts != 3 {
		t.Fatalf("原因/次数 = %s/%d, 期望 rejected/3", message.Reason, message.Attempts)
	}
}

// TestParseDeadLetterDefaults 验证无任何头时按死信队列命名还原来源队列并标记 unknown。
func TestParseDeadLetterDefaults(t *testing.T) {
	message := ParseDeadLetter("pulsefeed.clip.published.dead", amqp.Delivery{Body: []byte(`{}`)})
	if message.SourceQueue != "pulsefeed.clip.published" {
		t.Fatalf("来源队列 = %s, 期望去掉 .dead 后缀", message.SourceQueue)
	}
	if message.Reason != DeadReasonUnknown {
		t.Fatalf("原因 = %s, 期望 unknown", message.Reason)
	}
	if message.Attempts != 1 {
		t.Fatalf("处理次数 = %d, 期望至少 1", message.Attempts)
	}
}
