// worker.go 实现 PlayEventWorker：消费 impression.play_recorded 事件，
// 对 API 同步链路未完成曝光聚合的事件做二次 upsert 补偿（修正 D7：原项目只发不收）。
package appimpression

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	domimpression "pulsefeed/internal/domain/impression"
)

// PlayEventWorker 是 play_recorded 队列的消费者，保证曝光聚合最终一致。
type PlayEventWorker struct {
	repo domimpression.Repository
}

// NewPlayEventWorker 创建播放事件消费 Worker。
func NewPlayEventWorker(repo domimpression.Repository) *PlayEventWorker {
	return &PlayEventWorker{repo: repo}
}

// Name 返回 Worker 名称，供启动日志与指标区分。
func (w *PlayEventWorker) Name() string {
	return "play_event"
}

// Handle 解析事件并执行曝光补偿：
// 已聚合（synced=true）的事件直接确认；未聚合的执行 upsert 并标记同步；
// 非法载荷与领域终态错误直接确认，避免毒消息无限回队。
func (w *PlayEventWorker) Handle(ctx context.Context, body []byte) error {
	var event PlayRecorded
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("warning: 丢弃非法 play.recorded 载荷: %v", err)
		return nil
	}
	if event.EventID == "" || event.ViewerID <= 0 || event.ClipID <= 0 {
		log.Printf("warning: 丢弃字段非法的 play.recorded 载荷 (event_id=%s viewer_id=%d clip_id=%d)",
			event.EventID, event.ViewerID, event.ClipID)
		return nil
	}

	// API 同步链路在落库事务内已完成曝光聚合并标记 synced=true，正常路径直接确认。
	synced, err := w.repo.IsEventSynced(ctx, event.EventID)
	if err != nil {
		return err
	}
	if synced {
		return nil
	}

	// 补偿路径：把播放事件贡献累加进当日曝光聚合，并标记已同步。
	playEvent := &domimpression.PlayEvent{
		EventID:        event.EventID,
		ViewerID:       event.ViewerID,
		ClipID:         event.ClipID,
		PlayDurationMs: event.PlayDurationMs,
		Completed:      event.Completed,
		Impression:     event.Impression,
		CreatedAt:      event.OccurredAt,
	}
	if playEvent.CreatedAt.IsZero() {
		playEvent.CreatedAt = time.Now()
	}
	if err := w.repo.UpsertImpression(ctx, playEvent); err != nil {
		if errors.Is(err, domimpression.ErrClipNotFound) {
			// clip 已删除：重试无意义，确认消息。
			log.Printf("warning: play.recorded 因 clip 不可见被丢弃 (event_id=%s): %v", event.EventID, err)
			return nil
		}
		return err
	}
	if err := w.repo.MarkEventSynced(ctx, event.EventID); err != nil {
		return err
	}
	log.Printf("play_event 补偿曝光聚合完成 (event_id=%s viewer_id=%d clip_id=%d)",
		event.EventID, event.ViewerID, event.ClipID)
	return nil
}
