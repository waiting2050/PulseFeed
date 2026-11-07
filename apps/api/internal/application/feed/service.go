// service.go 编排 Feed 读取用例：场景分发、游标与页大小校验、分页判定与条目组装。
package appfeed

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"

	appobservability "pulsefeed/internal/application/observability"
	domfeed "pulsefeed/internal/domain/feed"

	"golang.org/x/sync/singleflight"
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

// Page 是一页策略读取结果。
// NextCursor 非空表示策略自行决定下一页游标（for-you 需携带 snapshot_at 与排序分数）；
// 为空时由 GetFeed 按 (published_at, clip_id) 兜底生成。
type Page struct {
	Entries    []*domfeed.FeedEntry
	NextCursor string
	HasMore    bool
}

// Strategy 描述一个 Feed 场景的取数策略，由 GetFeed 按场景分发。
type Strategy interface {
	// Scene 返回策略对应的场景标识。
	Scene() domfeed.Scene
	// Cacheable 报告该场景是否使用页缓存；following 等实时性场景返回 false 绕过页缓存。
	Cacheable() bool
	// Load 按游标读取一页条目（含多取的一条），count 已含 +1。
	Load(ctx context.Context, req *FeedRequest, cursor *domfeed.Cursor, count int) (*Page, error)
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

// Cacheable 报告 latest 场景使用页缓存。
func (s *LatestStrategy) Cacheable() bool {
	return true
}

// Load 走仓储按复合游标读取一页。
func (s *LatestStrategy) Load(ctx context.Context, _ *FeedRequest, cursor *domfeed.Cursor, count int) (*Page, error) {
	entries, err := s.repo.ListLatestPage(ctx, cursor, count)
	if err != nil {
		return nil, err
	}
	return &Page{Entries: entries}, nil
}

// FeedCache 描述 Feed 页缓存能力，由 infra/cache 实现；未注入或不可用时降级为纯 DB。
// GetPage 返回的条目每次反序列化独立副本，调用方可安全修改（组装读者状态）。
type FeedCache interface {
	// Enabled 报告缓存是否启用。
	Enabled() bool
	// GetPage 按页摘要读取缓存，ok=false 表示未命中。
	GetPage(ctx context.Context, digest string) (entries []*domfeed.FeedEntry, ok bool)
	// SetPage 回填页缓存；firstPage 为 true 使用首页短 TTL。
	SetPage(ctx context.Context, digest string, entries []*domfeed.FeedEntry, firstPage bool)
}

// Option 用于装配可选依赖。
type Option func(*Service)

// WithFeedCache 注入 Feed 页缓存，loadFeedPage 走 缓存 → singleflight → 仓储 → 回填。
func WithFeedCache(c FeedCache) Option {
	return func(s *Service) { s.cache = c }
}

// WithFollowingStrategy 注册 following 场景策略；cache 为 nil 时策略内部降级为 DB 回源。
func WithFollowingStrategy(cache FollowingCache) Option {
	return func(s *Service) {
		strategy := NewFollowingStrategy(s.repo, cache)
		s.strategies[strategy.Scene()] = strategy
	}
}

// WithForYouStrategy 注册 for-you 场景策略；推荐服务由组合根注入，策略负责卡片水合。
func WithForYouStrategy(recommender Recommender) Option {
	return func(s *Service) {
		strategy := NewForYouStrategy(s.repo, recommender)
		s.strategies[strategy.Scene()] = strategy
	}
}

// WithHotStrategy 注册 hot 场景策略；缓存不可用/窗口为空时策略内部回源 DB。
func WithHotStrategy(cache HotCache) Option {
	return func(s *Service) {
		strategy := NewHotStrategy(s.repo, cache)
		s.strategies[strategy.Scene()] = strategy
	}
}

// WithMetrics 注入可观测性端口，记录 Feed 场景结果与页缓存命中情况。
func WithMetrics(metrics appobservability.Metrics) Option {
	return func(s *Service) { s.metrics = metrics }
}

// Service 提供 Feed 读取用例。
type Service struct {
	repo       domfeed.Repository
	strategies map[domfeed.Scene]Strategy
	cache      FeedCache
	group      singleflight.Group
	metrics    appobservability.Metrics
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

// GetFeed 记录场景结果与耗时后调用 getFeed。
func (s *Service) GetFeed(ctx context.Context, req *FeedRequest) (*FeedResult, error) {
	start := time.Now()
	result, err := s.getFeed(ctx, req)
	if s.metrics != nil {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		s.metrics.ObserveFeed(req.Scene, outcome, time.Since(start))
	}
	return result, err
}

// getFeed 按场景分发读取一页 Feed：解析场景与游标、校验页大小、判定 has_more 并组装条目。
func (s *Service) getFeed(ctx context.Context, req *FeedRequest) (*FeedResult, error) {
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
	page, err := s.loadFeedPage(ctx, strategy, req, cursor, limit+1)
	if err != nil {
		return nil, err
	}

	result := &FeedResult{Items: page.Entries, NextCursor: page.NextCursor, HasMore: page.HasMore}
	if len(result.Items) > limit {
		result.HasMore = true
		result.Items = result.Items[:limit]
		if result.NextCursor == "" {
			last := result.Items[len(result.Items)-1]
			result.NextCursor = domfeed.EncodeCursor(&domfeed.Cursor{PublishedAt: last.PublishedAt, ClipID: last.ClipID})
		}
	}

	if err := s.assembleFeedEntries(ctx, req.ViewerID, result.Items); err != nil {
		return nil, err
	}
	return result, nil
}

// loadFeedPage 读取一页条目：可缓存场景缓存命中直接返回，未命中经 singleflight 合并并发回源，
// 仓储读出后回填缓存；缓存未启用/不可用或场景声明不缓存（following）时降级为纯仓储读取。
func (s *Service) loadFeedPage(ctx context.Context, strategy Strategy, req *FeedRequest, cursor *domfeed.Cursor, count int) (*Page, error) {
	if s.cache == nil || !s.cache.Enabled() || !strategy.Cacheable() {
		s.observeCache("disabled")
		return strategy.Load(ctx, req, cursor, count)
	}

	// count 比页大小多 1，key 还原为页大小以符合 C5 的 limit 语义。
	digest := pageDigest(strategy.Scene(), req.ViewerID, req.Cursor, count-1)
	if entries, ok := s.cache.GetPage(ctx, digest); ok {
		s.observeCache("hit")
		return &Page{Entries: entries}, nil
	}
	s.observeCache("miss")

	value, err, _ := s.group.Do(digest, func() (any, error) {
		// 单飞窗口内二次读缓存：等待期间可能已有并发请求回填。
		if entries, ok := s.cache.GetPage(ctx, digest); ok {
			return &Page{Entries: entries}, nil
		}
		page, err := strategy.Load(ctx, req, cursor, count)
		if err != nil {
			return nil, err
		}
		s.cache.SetPage(ctx, digest, page.Entries, cursor == nil)
		return page, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*Page), nil
}

// observeCache 记录一次页缓存访问结果；未注入指标时静默跳过。
func (s *Service) observeCache(result string) {
	if s.metrics != nil {
		s.metrics.ObserveCache(result)
	}
}

// pageDigest 按 C5 约定计算页缓存键摘要：sha1(scene|viewer|cursor|limit) 取前 16 位 hex。
func pageDigest(scene domfeed.Scene, viewerID int64, cursor string, limit int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%s|%d", scene, viewerID, cursor, limit)))
	return hex.EncodeToString(sum[:])[:16]
}

// assembleFeedEntries 填充条目的读者互动状态：每次请求实时读库，未登录读者直接跳过。
func (s *Service) assembleFeedEntries(ctx context.Context, viewerID int64, entries []*domfeed.FeedEntry) error {
	// 页缓存与 singleflight 会在并发请求间共享同一批条目指针，
	// 组装前逐个拷贝并清空读者状态，避免把某个读者的 liked/saved 写进共享数据。
	for i, entry := range entries {
		clone := *entry
		clone.Viewer = domfeed.ViewerState{}
		entries[i] = &clone
	}

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
