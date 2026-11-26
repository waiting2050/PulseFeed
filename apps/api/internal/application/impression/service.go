// service.go 编排播放事件上报用例：校验 → 幂等落库（含当日曝光聚合）→ 投递 play.recorded 事件。
// 启用 outbox 时事件与落库同事务写入，由 relay 经 publisher confirm 投递，失败按退避重试；
// 未启用时沿用直接投递，投递失败不回滚落库结果，Worker 消费链路保证曝光最终一致。
package appimpression

import (
	"context"
	"encoding/json"
	"fmt"
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

// WithOutbox 启用事务性 outbox：play.recorded 事件与播放事件同事务落库，
// 由 Worker 的 relay 经 publisher confirm 投递，避免“落库成功但消息丢失”。
func WithOutbox() Option {
	return func(s *Service) {
		s.outbox = true
	}
}

// Service 提供曝光相关用例。
type Service struct {
	repo      domimpression.Repository
	publisher PlayEventPublisher
	outbox    bool
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

	if s.outbox {
		// 事件与播放事件同事务落库：落库成功即一定被 relay 投递；重复 event_id 不产生新事件。
		_, err := s.repo.RecordPlayEventWithEvents(ctx, event, func(isNew bool) ([]domimpression.OutboxEvent, error) {
			if !isNew {
				return nil, nil
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return nil, fmt.Errorf("序列化 play.recorded 事件失败: %w", err)
			}
			return []domimpression.OutboxEvent{{
				EventID: event.EventID,
				Topic:   TopicPlayRecorded,
				Payload: body,
			}}, nil
		})
		return err
	}

	// 落库（含当日曝光 upsert）；同 event_id 重复上报为 no-op。
	if _, err := s.repo.RecordPlayEvent(ctx, event); err != nil {
		return err
	}

	// 投递事件供 Worker 做曝光补偿；失败仅记日志，不影响接口返回。
	if s.publisher != nil {
		if err := s.publisher.PublishPlayRecorded(ctx, payload); err != nil {
			log.Printf("投递 play.recorded 事件失败 (event_id=%s): %v", event.EventID, err)
		}
	}
	return nil
}

// now 取当前时间，便于测试注入；生产环境直接使用 time.Now。
var now = time.Now
