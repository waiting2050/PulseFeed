// service.go 编排 Feed 读取用例：场景分发、游标与页大小校验、分页判定与条目组装。
package appfeed

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
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
	// GetPage 按页摘要读取缓存；ok=false 表示未命中，err 非 nil 表示缓存故障。
	GetPage(ctx context.Context, digest string) (entries []*domfeed.FeedEntry, ok bool, err error)
	// SetPage 回填页缓存；firstPage 为 true 使用首页短 TTL。
	SetPage(ctx context.Context, digest string, entries []*domfeed.FeedEntry, firstPage bool)
	// SetPageEmpty 为空结果写短 TTL 负标记，避免同一 digest 在回源空页后反复击穿。
	SetPageEmpty(ctx context.Context, digest string)
	// FeedVersion 返回页缓存版本号；删除内容后递增版本即可整体失效既有页缓存。
	// 读取故障返回错误，调用方应跳过页缓存而不是用默认版本构造摘要。
	FeedVersion(ctx context.Context) (string, error)
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

// WithCursorCodec 注入带服务端签名的游标编解码器；未注入时保持无签名的旧行为（仅限测试装配）。
func WithCursorCodec(codec *domfeed.CursorCodec) Option {
	return func(s *Service) { s.cursors = codec }
}

// Service 提供 Feed 读取用例。
type Service struct {
	repo       domfeed.Repository
	strategies map[domfeed.Scene]Strategy
	cache      FeedCache
	cursors    *domfeed.CursorCodec
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
	// 指标在选项全部应用后统一注入策略，避免选项顺序导致策略拿不到指标。
	for _, strategy := range s.strategies {
		if setter, ok := strategy.(interface {
			setMetrics(appobservability.Metrics)
		}); ok {
			setter.setMetrics(s.metrics)
		}
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
	cursor, legacy, err := s.decodeCursor(req.Cursor)
	if err != nil {
		return nil, err
	}
	if legacy {
		// 签名上线前的旧游标没有服务端签名，无法纳入受控的页缓存键空间；
		// 与 Version 过旧一致降级为首屏并记日志，避免旧客户端拿到 500。
		log.Printf("info: 收到无签名旧游标，重置为首屏")
		cursor = nil
	} else if scene == domfeed.SceneFollowing && cursor != nil && cursor.Version < domfeed.CursorVersionMilli {
		// 旧客户端持有毫秒改造前生成的秒级游标：其时间边界无法与新毫秒边界可靠比较，
		// 继续使用会漏掉同一秒内更早发布的内容。显式降级为首屏而不是返回错误，
		// 保证旧游标不会触发 500；客户端会回到第一页重新开始翻页。
		log.Printf("info: following 游标版本过旧，重置为首屏 (version=%d)", cursor.Version)
		cursor = nil
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
			result.NextCursor = s.encodeCursor(&domfeed.Cursor{
				PublishedAt: last.PublishedAt,
				ClipID:      last.ClipID,
				Version:     domfeed.CursorVersionMilli,
			})
		}
	}

	// 策略自行生成的游标（hot/for-you）是未签名载荷，统一在出口补充签名，
	// 保证客户端拿到的游标只能由本服务生成。
	if result.NextCursor != "" {
		result.NextCursor = s.resignCursor(result.NextCursor)
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

	// 版本号参与摘要，删除内容后递增版本即可让旧页缓存不再命中。
	// 版本读取失败时无法构造可靠摘要，直接跳过页缓存：若退化为默认版本，
	// 可能命中删除前写入的版本 0 旧页，导致已删除内容短暂复现。
	version, err := s.cache.FeedVersion(ctx)
	if err != nil {
		log.Printf("warning: 读取 feed 缓存版本失败，跳过页缓存回源 DB: %v", err)
		s.observeCache("error")
		return strategy.Load(ctx, req, cursor, count)
	}

	// count 比页大小多 1，key 还原为页大小以符合 C5 的 limit 语义。
	// 摘要用规范化游标而不是 req.Cursor 原文：同一逻辑游标的不同文本编码必须落到同一个键，
	// 否则任意改写游标字符串都会扩散出新的缓存键。签名校验已在 getFeed 完成。
	digest := pageDigest(strategy.Scene(), req.ViewerID, domfeed.EncodeCursor(cursor), count-1, version)
	entries, ok, err := s.cache.GetPage(ctx, digest)
	if err != nil {
		// 缓存故障与未命中区分：直接回源且不回填，避免把故障期结果写进缓存。
		log.Printf("warning: 读取 feed 页缓存失败，回源 DB 且跳过回填: %v", err)
		s.observeCache("error")
		return strategy.Load(ctx, req, cursor, count)
	}
	if ok {
		s.observeCache("hit")
		return &Page{Entries: entries}, nil
	}
	s.observeCache("miss")

	value, err, _ := s.group.Do(digest, func() (any, error) {
		// 单飞窗口内二次读缓存：等待期间可能已有并发请求回填。
		cacheFault := false
		if entries, ok, err := s.cache.GetPage(ctx, digest); err != nil {
			log.Printf("warning: 读取 feed 页缓存失败，回源 DB 且跳过回填: %v", err)
			cacheFault = true
		} else if ok {
			return &Page{Entries: entries}, nil
		}
		page, err := strategy.Load(ctx, req, cursor, count)
		if err != nil {
			return nil, err
		}
		if !cacheFault {
			// 空页写短 TTL 负标记而不是普通页：既吸收同一 key 的连续重放，
			// 又避免空结论按翻页 TTL 挡住之后发布的新内容。
			if len(page.Entries) == 0 {
				s.cache.SetPageEmpty(ctx, digest)
			} else {
				s.cache.SetPage(ctx, digest, page.Entries, cursor == nil)
			}
		}
		return page, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*Page), nil
}

// decodeCursor 按装配情况解析游标：注入签名器时校验签名并识别无签名旧游标；
// 未注入时保持原来的无签名解析（单元测试与内部装配）。
func (s *Service) decodeCursor(token string) (*domfeed.Cursor, bool, error) {
	if s.cursors == nil {
		cursor, err := domfeed.DecodeCursor(token)
		return cursor, false, err
	}
	return s.cursors.Decode(token)
}

// encodeCursor 按装配情况编码游标，保证出口游标与入口校验使用同一把密钥。
func (s *Service) encodeCursor(cursor *domfeed.Cursor) string {
	if s.cursors == nil {
		return domfeed.EncodeCursor(cursor)
	}
	return s.cursors.Encode(cursor)
}

// resignCursor 把策略产出的（未签名）游标统一重新编码，使出口游标始终带签名；
// 已签名的游标解码后再签名结果不变，因此可安全重复调用。
func (s *Service) resignCursor(token string) string {
	cursor, _, err := s.decodeCursor(token)
	if err != nil || cursor == nil {
		return token
	}
	return s.encodeCursor(cursor)
}

// observeCache 记录一次页缓存访问结果；未注入指标时静默跳过。
func (s *Service) observeCache(result string) {
	if s.metrics != nil {
		s.metrics.ObserveCache(result)
	}
}

// pageDigest 按 C5 约定计算页缓存键摘要：sha1(scene|viewer|cursor|limit|version) 取前 16 位 hex。
func pageDigest(scene domfeed.Scene, viewerID int64, cursor string, limit int, version string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%s|%d|%s", scene, viewerID, cursor, limit, version)))
	return hex.EncodeToString(sum[:])[:16]
}

// assembleFeedEntries 填充条目的作者摘要与读者互动状态：每次请求实时读库；
// 作者昵称/头像对未登录读者也补齐，关注标记仅在有读者身份时读取。
func (s *Service) assembleFeedEntries(ctx context.Context, viewerID int64, entries []*domfeed.FeedEntry) error {
	// 页缓存与 singleflight 会在并发请求间共享同一批条目指针，
	// 组装前逐个拷贝并清空读者相关状态，避免把某个读者的 liked/saved/following 写进共享数据。
	for i, entry := range entries {
		clone := *entry
		clone.Viewer = domfeed.ViewerState{}
		clone.Creator = domfeed.CreatorSummary{UserID: entry.CreatorID}
		entries[i] = &clone
	}

	if len(entries) == 0 {
		return nil
	}

	creatorIDs := make([]int64, 0, len(entries))
	seenCreators := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		if _, ok := seenCreators[entry.CreatorID]; ok {
			continue
		}
		seenCreators[entry.CreatorID] = struct{}{}
		creatorIDs = append(creatorIDs, entry.CreatorID)
	}
	profiles, err := s.repo.BatchGetCreatorProfiles(ctx, creatorIDs)
	if err != nil {
		return err
	}
	followed := map[int64]bool{}
	if viewerID != 0 {
		followed, err = s.repo.BatchGetFollowedCreatorIDs(ctx, viewerID, creatorIDs)
		if err != nil {
			return err
		}
	}
	for _, entry := range entries {
		if profile, ok := profiles[entry.CreatorID]; ok {
			entry.Creator.Nickname = profile.Nickname
			entry.Creator.AvatarURL = profile.AvatarURL
		}
		entry.Creator.Following = followed[entry.CreatorID]
	}

	if viewerID == 0 {
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
