// event.go 定义 impression.play_recorded 事件载荷与事件发布端口，应用层不感知 MQ 实现。
package appimpression

import (
	"context"
	"time"
)

// PlayRecorded 是播放事件上报成功后投递的事件载荷，由 PlayEventWorker 消费做聚合补偿。
type PlayRecorded struct {
	EventID    string    `json:"event_id"`
	ViewerID   int64     `json:"viewer_id"`
	ClipID     int64     `json:"clip_id"`
	EventType  string    `json:"event_type"`
	Scene      string    `json:"scene"`
	RequestID  string    `json:"request_id"`
	WatchMs    int64     `json:"watch_ms"`
	Completed  bool      `json:"completed"`
	Impression bool      `json:"impression"`
	OccurredAt time.Time `json:"occurred_at"`
}

// TopicPlayRecorded 是播放事件补偿的 outbox 主题，relay 据此投递到曝光交换机。
const TopicPlayRecorded = "impression.play_recorded"

// PlayEventPublisher 是 play.recorded 事件发布端口，由 infra/mq 实现并经组合根注入；
// 未注入时上报成功但不投递事件（Worker 无法补偿，仅靠 API 同步链路保证一致性）。
type PlayEventPublisher interface {
	// PublishPlayRecorded 投递 impression.play_recorded 事件。
	PublishPlayRecorded(ctx context.Context, event PlayRecorded) error
}
