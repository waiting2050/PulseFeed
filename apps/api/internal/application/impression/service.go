// service.go 编排播放事件上报用例：校验 → 幂等落库（含当日曝光聚合）→ 投递 play.recorded 事件。
// 事件投递失败不回滚落库结果，Worker 消费链路保证曝光最终一致。
package appimpression

import (
	"context"
	"log"
	"time"

	domimpression "pulsefeed/internal/domain/impression"
)

// Option 用于装配可选依赖。
type Option func(*Service)

// WithPublisher 注入 play.recorded 事件发布器；未注入时上报成功但不投递事件。
func WithPublisher(p PlayEventPublisher) Option {
	return func(s *Service) {
		s.publisher = p
	}
}

// Service 提供曝光相关用例。
type Service struct {
	repo      domimpression.Repository
	publisher PlayEventPublisher
}

// New 创建曝光应用服务。
func New(repo domimpression.Repository, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// RecordPlayEvent 上报一条展示/播放事件：幂等落库并按事件类型更新当日聚合，随后异步投递事件供 Worker 补偿。
func (s *Service) RecordPlayEvent(ctx context.Context, in domimpression.PlayEventInput) error {
	event, err := domimpression.NewPlayEvent(in)
	if err != nil {
		return err
	}

	// 落库（含当日曝光 upsert）；同 event_id 重复上报为 no-op。
	if _, err := s.repo.RecordPlayEvent(ctx, event); err != nil {
		return err
	}

	// 投递事件供 Worker 做曝光补偿；失败仅记日志，不影响接口返回。
	if s.publisher != nil {
		payload := PlayRecorded{
			EventID:    event.EventID,
			ViewerID:   event.ViewerID,
			ClipID:     event.ClipID,
			EventType:  string(event.EventType),
			Scene:      event.Scene,
			RequestID:  event.RequestID,
			WatchMs:    event.WatchMs,
			Completed:  event.Completed,
			Impression: event.Impression,
			OccurredAt: event.CreatedAt,
		}
		if err := s.publisher.PublishPlayRecorded(ctx, payload); err != nil {
			log.Printf("投递 play.recorded 事件失败 (event_id=%s): %v", event.EventID, err)
		}
	}
	return nil
}

// now 取当前时间，便于测试注入；生产环境直接使用 time.Now。
var now = time.Now
