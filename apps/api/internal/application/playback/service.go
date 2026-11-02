// service.go 编排 playback 应用用例：播放配置读取/更新、预加载列表与 QoS 上报（幂等）。
package appplayback

import (
	"context"
	"errors"

	appobservability "pulsefeed/internal/application/observability"
	domplayback "pulsefeed/internal/domain/playback"
)

// ClipProvider 描述预加载所需的 clip ID 数据源；默认注入 feed 仓储的 ListLatestIDs。
type ClipProvider interface {
	ListLatestIDs(ctx context.Context, limit int) ([]int64, error)
}

// ClipProviderFunc 把普通函数适配为 ClipProvider 接口，供组合根桥接不同签名的仓储方法。
type ClipProviderFunc func(ctx context.Context, limit int) ([]int64, error)

// ListLatestIDs 实现 ClipProvider 接口。
func (f ClipProviderFunc) ListLatestIDs(ctx context.Context, limit int) ([]int64, error) {
	return f(ctx, limit)
}

// Service 提供 playback 应用用例。
type Service struct {
	repo    domplayback.Repository
	clips   ClipProvider
	metrics appobservability.Metrics
}

// Option 是 playback 服务的可选装配项。
type Option func(*Service)

// WithMetrics 注入可观测性端口；nil 表示不采集指标。
func WithMetrics(metrics appobservability.Metrics) Option {
	return func(s *Service) {
		s.metrics = metrics
	}
}

// New 创建 playback 应用服务。repo 由组合根注入，clips 可为 nil 但 GetPreload 会返回空列表。
func New(repo domplayback.Repository, clips ClipProvider, opts ...Option) *Service {
	s := &Service{repo: repo, clips: clips}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// GetConfig 按用户 ID 读取播放配置；未命中返回归一化后的默认值。
func (s *Service) GetConfig(ctx context.Context, userID int64) (*domplayback.Config, error) {
	if userID <= 0 {
		return nil, domplayback.ErrInvalidConfigUser
	}
	return s.repo.GetOrCreateConfig(ctx, userID)
}

// UpsertConfig 写入或更新播放配置；返回落库值。
func (s *Service) UpsertConfig(ctx context.Context, userID int64, preloadCount int, sampleRate float64) (*domplayback.Config, error) {
	if userID <= 0 {
		return nil, domplayback.ErrInvalidConfigUser
	}
	cfg := &domplayback.Config{
		UserID:        userID,
		PreloadCount:  preloadCount,
		QoSSampleRate: sampleRate,
	}
	cfg.Normalize()
	return s.repo.UpsertConfig(ctx, cfg)
}

// GetPreload 按用户配置返回预加载 clip ID 列表；依赖 ClipProvider；配置缺失或 provider nil 都返回空切片 + nil 错误。
func (s *Service) GetPreload(ctx context.Context, userID int64) ([]int64, error) {
	if userID <= 0 {
		return nil, domplayback.ErrInvalidConfigUser
	}
	cfg, err := s.repo.GetOrCreateConfig(ctx, userID)
	if err != nil {
		return nil, err
	}
	if s.clips == nil {
		return []int64{}, nil
	}
	ids, err := s.clips.ListLatestIDs(ctx, cfg.PreloadCount)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}

// ReportQoS 幂等上报一次 QoS；重复 event_id 不二次落库。
func (s *Service) ReportQoS(ctx context.Context, reporterID int64, report *domplayback.QoSReport) (inserted bool, err error) {
	if report == nil {
		return false, errors.New("nil qos report")
	}
	if reporterID <= 0 {
		return false, domplayback.ErrInvalidReportTarget
	}
	report.ReporterID = reporterID
	if err := report.Validate(); err != nil {
		return false, err
	}
	inserted, err = s.repo.CreateQoS(ctx, report)
	if s.metrics != nil {
		result := "duplicate"
		switch {
		case err != nil:
			result = "error"
		case inserted:
			result = "inserted"
		}
		s.metrics.ObserveQoS(result, report.LatencyMS, report.StallCount, report.Completed)
	}
	return inserted, err
}
