// service.go 编排 Feed 读取用例：场景分发、游标与页大小校验、分页判定与条目组装。
package appfeed

import (
	"context"

	domfeed "pulsefeed/internal/domain/feed"
)

// FeedRequest 是一次 Feed 读取请求；ViewerID 为 0 表示未登录读者。
type FeedRequest struct {
	Scene    string
	ViewerID int64
	Cursor   string
	Limit    int
}

// FeedResult 是 Feed 分页结果。
type FeedResult struct {
	Items      []*domfeed.FeedEntry
	NextCursor string
	HasMore    bool
}

// Strategy 描述一个 Feed 场景的取数策略，由 GetFeed 按场景分发。
type Strategy interface {
	// Scene 返回策略对应的场景标识。
	Scene() domfeed.Scene
	// Load 按游标读取一页条目（含多取的一条），count 已含 +1。
	Load(ctx context.Context, req *FeedRequest, cursor *domfeed.Cursor, count int) ([]*domfeed.FeedEntry, error)
}

// LatestStrategy 是 latest 场景策略：纯数据库按 (published_at, id) 递减分页。
type LatestStrategy struct {
	repo domfeed.Repository
}

// NewLatestStrategy 创建 latest 场景策略。
func NewLatestStrategy(repo domfeed.Repository) *LatestStrategy {
	return &LatestStrategy{repo: repo}
}

// Scene 返回 latest 场景标识。
func (s *LatestStrategy) Scene() domfeed.Scene {
	return domfeed.SceneLatest
}

// Load 走仓储按复合游标读取一页。
func (s *LatestStrategy) Load(ctx context.Context, _ *FeedRequest, cursor *domfeed.Cursor, count int) ([]*domfeed.FeedEntry, error) {
	return s.repo.ListLatestPage(ctx, cursor, count)
}

// Option 用于装配可选依赖。
type Option func(*Service)

// Service 提供 Feed 读取用例。
type Service struct {
	repo       domfeed.Repository
	strategies map[domfeed.Scene]Strategy
}

// New 创建 feed 应用服务，默认注册 latest 策略；其余场景策略随后续阶段接入。
func New(repo domfeed.Repository, opts ...Option) *Service {
	latest := NewLatestStrategy(repo)
	s := &Service{
		repo:       repo,
		strategies: map[domfeed.Scene]Strategy{latest.Scene(): latest},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// GetFeed 按场景分发读取一页 Feed：解析场景与游标、校验页大小、判定 has_more 并组装条目。
func (s *Service) GetFeed(ctx context.Context, req *FeedRequest) (*FeedResult, error) {
	scene, err := domfeed.ParseScene(req.Scene)
	if err != nil {
		return nil, err
	}
	if req.Limit < 0 || req.Limit > domfeed.MaxFeedLimit {
		return nil, domfeed.ErrInvalidLimit
	}
	limit := req.Limit
	if limit == 0 {
		limit = domfeed.DefaultFeedLimit
	}
	cursor, err := domfeed.DecodeCursor(req.Cursor)
	if err != nil {
		return nil, err
	}

	strategy := s.strategies[scene]
	// 多取一条用于判定是否还有下一页，返回前截断。
	entries, err := s.loadFeedPage(ctx, strategy, req, cursor, limit+1)
	if err != nil {
		return nil, err
	}

	result := &FeedResult{Items: entries}
	if len(entries) > limit {
		result.HasMore = true
		result.Items = entries[:limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = domfeed.EncodeCursor(&domfeed.Cursor{PublishedAt: last.PublishedAt, ClipID: last.ClipID})
	}

	if err := s.assembleFeedEntries(ctx, req.ViewerID, result.Items); err != nil {
		return nil, err
	}
	return result, nil
}

// loadFeedPage 经策略读取一页条目；S8 将在此接入页缓存与 singleflight 回源。
func (s *Service) loadFeedPage(ctx context.Context, strategy Strategy, req *FeedRequest, cursor *domfeed.Cursor, count int) ([]*domfeed.FeedEntry, error) {
	return strategy.Load(ctx, req, cursor, count)
}

// assembleFeedEntries 填充条目的读者互动状态；S9 前仓储返回空集，未登录读者直接跳过。
func (s *Service) assembleFeedEntries(ctx context.Context, viewerID int64, entries []*domfeed.FeedEntry) error {
	if viewerID == 0 || len(entries) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ClipID)
	}
	states, err := s.repo.BatchGetViewerStates(ctx, viewerID, ids)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if state, ok := states[entry.ClipID]; ok {
			entry.Viewer = *state
		}
	}
	return nil
}
