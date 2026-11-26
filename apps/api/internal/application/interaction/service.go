// service.go 编排点赞/收藏状态变更与评论增删查用例，只依赖互动领域端口；
// 装配异步流水线后走「Redis 先写 → MQ 投递 → Worker 落库」，失败自动回退同步落库。
package appaction

import (
	"context"
	"log"
	"time"

	domfeed "pulsefeed/internal/domain/feed"
	dominteraction "pulsefeed/internal/domain/interaction"
)

// CommentListResult 是评论列表的分页结果。
type CommentListResult struct {
	Items      []*dominteraction.Comment
	NextCursor string
	HasMore    bool
}

// ActionChanged 是 interaction.action_changed 事件载荷，由 ActionWorker 消费落库。
type ActionChanged struct {
	UserID         int64                     `json:"user_id"`
	ClipID         int64                     `json:"clip_id"`
	ActionType     dominteraction.ActionType `json:"action_type"`
	Active         bool                      `json:"active"`
	IdempotencyKey string                    `json:"idempotency_key,omitempty"`
	OccurredAt     time.Time                 `json:"occurred_at"`
}

// TopicActionChanged 是 interaction.action_changed 事件的逻辑类型，用于死信消息归类。
const TopicActionChanged = "interaction.action_changed"

// ActionStateCache 是互动状态的 Redis 先行写入端口，由 infra/cache 实现。
type ActionStateCache interface {
	// SetActionState 更新读者行为状态与分片计数，返回该 clip 的最新计数。
	SetActionState(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool) (*dominteraction.ActionResult, error)
}

// ActionEventPublisher 是 action_changed 事件投递端口，由 infra/mq 实现。
type ActionEventPublisher interface {
	PublishActionChanged(ctx context.Context, event ActionChanged) error
}

// HotScoreRecorder 是热度分钟桶写入端口，由 infra/cache 实现（S15）。
// 仅在互动计数真正变化时累加，递减/幂等命中不写桶；返回错误供调用方记录失败指标。
type HotScoreRecorder interface {
	RecordHotScore(ctx context.Context, clipID int64, delta float64) error
}

// 互动副作用的 kind 标签，对应 pulsefeed_interaction_side_effects_total。
const (
	sideEffectHotScore      = "hot_score"
	sideEffectActionEvent   = "action_event"
	sideEffectLikeNotify    = "like_notify"
	sideEffectCommentNotify = "comment_notify"
)

// InteractionMetrics 是互动链路的可观测性端口；未注入时所有调用为空操作。
type InteractionMetrics interface {
	// ObserveInteractionSideEffect 记录一次提交后副作用的执行结果，result 取 ok/error。
	ObserveInteractionSideEffect(kind, result string)
	// ObserveInteractionLimiterError 记录一次互动写限流降级（Redis 不可用 fail-open）。
	ObserveInteractionLimiterError(scope string)
}

// ActionRateLimiter 是互动写接口的用户级限流端口，由 infra/cache 的固定窗口实现；
// 返回 (allowed, retryAfter, err)：err 非 nil 时调用方 fail-open，不允许用户因限流器故障被拒绝。
type ActionRateLimiter interface {
	AllowUser(ctx context.Context, userID int64) (bool, time.Duration, error)
}

// WithRateLimiter 注入互动写限流器；未注入时不限流。
func WithRateLimiter(limiter ActionRateLimiter) Option {
	return func(s *Service) {
		s.rateLimiter = limiter
	}
}

// enforceWriteLimit 在写接口入口做用户级固定窗口校验。
// Redis 故障时 fail-open 只记日志与指标，保证限流器不可用不会让用户点不了赞。
func (s *Service) enforceWriteLimit(ctx context.Context, userID int64) error {
	if s.rateLimiter == nil {
		return nil
	}
	allowed, retryAfter, err := s.rateLimiter.AllowUser(ctx, userID)
	if err != nil {
		log.Printf("warning: 互动写限流降级（Redis 故障，fail-open, user_id=%d）: %v", userID, err)
		if s.metrics != nil {
			s.metrics.ObserveInteractionLimiterError("user")
		}
		return nil
	}
	if !allowed {
		return &dominteraction.ErrActionRateLimited{RetryAfter: retryAfter}
	}
	return nil
}

// WithMetrics 注入互动指标；未注入时副作用失败只记日志。
func WithMetrics(metrics InteractionMetrics) Option {
	return func(s *Service) {
		s.metrics = metrics
	}
}

// observeSideEffect 把副作用结果归类为 ok/error 并上报；metrics 为 nil 时静默跳过。
func observeSideEffect(metrics InteractionMetrics, kind string, err error) {
	if metrics == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	metrics.ObserveInteractionSideEffect(kind, result)
}

// MessageNotifier 是点赞/评论通知的投递端口，由 message 应用服务实现。
type MessageNotifier interface {
	NotifyLiked(ctx context.Context, actorID, clipID, creatorID int64) error
	NotifyCommented(ctx context.Context, actorID, clipID, commentID, creatorID int64, content string) error
}

// ClipOwnerResolver 用于解析作品创作者；互动服务只依赖这一最小接口，避免耦合 clip 仓储全貌。
type ClipOwnerResolver interface {
	OwnerID(ctx context.Context, clipID int64) (int64, error)
}

// ClipOwnerFunc 把函数适配为 ClipOwnerResolver，便于组合根桥接 clip 仓储。
type ClipOwnerFunc func(ctx context.Context, clipID int64) (int64, error)

// OwnerID 实现 ClipOwnerResolver。
func (f ClipOwnerFunc) OwnerID(ctx context.Context, clipID int64) (int64, error) {
	return f(ctx, clipID)
}

// WithMessageNotifier 注入消息通知器与创作者解析器；未注入时互动仍正常工作，只是不产生通知。
func WithMessageNotifier(notifier MessageNotifier, owners ClipOwnerResolver) Option {
	return func(s *Service) {
		s.notifier = notifier
		s.owners = owners
	}
}

// Option 用于装配可选依赖。
type Option func(*Service)

// WithAsyncActionPipeline 启用异步互动链路：缓存先行写入成功后投递事件，
// 任一环节失败都回退到仓储同步落库，保证接口不丢写。
func WithAsyncActionPipeline(states ActionStateCache, publisher ActionEventPublisher) Option {
	return func(s *Service) {
		s.stateCache = states
		s.publisher = publisher
	}
}

// WithHotScoreRecorder 注入热度分钟桶写入器；缓存未启用时注入的实现自身静默跳过。
func WithHotScoreRecorder(recorder HotScoreRecorder) Option {
	return func(s *Service) { s.hotScores = recorder }
}

// Service 提供互动相关用例。
type Service struct {
	repo        dominteraction.Repository
	stateCache  ActionStateCache
	publisher   ActionEventPublisher
	hotScores   HotScoreRecorder
	notifier    MessageNotifier
	owners      ClipOwnerResolver
	metrics     InteractionMetrics
	rateLimiter ActionRateLimiter
}

// New 创建互动应用服务。
func New(repo dominteraction.Repository, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Like 点赞：行为置为有效并递增 like_count。
func (s *Service) Like(ctx context.Context, userID, clipID int64, idempotencyKey string) (*dominteraction.ActionResult, error) {
	return s.setAction(ctx, userID, clipID, dominteraction.ActionLike, true, idempotencyKey)
}

// Unlike 取消点赞：行为置为取消并递减 like_count（下限 0）。
func (s *Service) Unlike(ctx context.Context, userID, clipID int64, idempotencyKey string) (*dominteraction.ActionResult, error) {
	return s.setAction(ctx, userID, clipID, dominteraction.ActionLike, false, idempotencyKey)
}

// Save 收藏：行为置为有效并递增 save_count。
func (s *Service) Save(ctx context.Context, userID, clipID int64, idempotencyKey string) (*dominteraction.ActionResult, error) {
	return s.setAction(ctx, userID, clipID, dominteraction.ActionSave, true, idempotencyKey)
}

// Unsave 取消收藏：行为置为取消并递减 save_count（下限 0）。
func (s *Service) Unsave(ctx context.Context, userID, clipID int64, idempotencyKey string) (*dominteraction.ActionResult, error) {
	return s.setAction(ctx, userID, clipID, dominteraction.ActionSave, false, idempotencyKey)
}

// setAction 是四类状态变更的公共入口；异步链路任一环节失败时回退仓储同步落库。
//
// 事务边界：仓储事务只负责 pf_actions 与 pf_clip_stats 这类必须一致的行；
// 热度分钟桶、创作者通知与 action_changed 事件投递都是非正确性写入，放在持久化返回之后执行。
// 提交后副作用失败不回滚、不影响响应，只记日志与指标（pulsefeed_interaction_side_effects_total）；
// 取舍是：用户已看到的成功互动不会因热度/通知失败而失败，代价是热度可能短暂缺失，
// 需要依赖该错误指标告警，并通过同一幂等键重试或后续同向互动补偿。
func (s *Service) setAction(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, error) {
	if clipID <= 0 {
		return nil, dominteraction.ErrClipNotFound
	}
	if err := s.enforceWriteLimit(ctx, userID); err != nil {
		return nil, err
	}
	var result *dominteraction.ActionResult
	var err error
	async := false
	if s.stateCache != nil && s.publisher != nil {
		result, err = s.tryAsyncAction(ctx, userID, clipID, actionType, active, idempotencyKey)
		// 异步链路成功时 DB 写入由 ActionWorker 完成，热度随落库在 Worker 侧记录。
		async = result != nil && err == nil
	}
	if result == nil && err == nil {
		result, err = s.repo.SetAction(ctx, userID, clipID, actionType, active, idempotencyKey)
	}
	if err != nil {
		return nil, err
	}
	if !async {
		// 同步链路此时事务已提交，热度在请求内记录；失败只计指标，不影响返回值。
		s.recordHotScore(ctx, clipID, actionType, result)
	}
	// 点赞真正生效时向创作者投递通知；取消点赞不撤回历史消息，重复点赞由消息幂等键去重。
	if result.Changed && result.Active && actionType == dominteraction.ActionLike {
		s.notifyLike(ctx, userID, clipID)
	}
	return result, nil
}

// notifyLike 解析作品创作者并投递点赞通知；通知失败只记录日志，不影响互动主链路。
func (s *Service) notifyLike(ctx context.Context, userID, clipID int64) {
	if s.notifier == nil || s.owners == nil {
		return
	}
	creatorID, err := s.owners.OwnerID(ctx, clipID)
	if err != nil {
		log.Printf("warning: 点赞通知解析创作者失败 (clip_id=%d): %v", clipID, err)
		return
	}
	if creatorID <= 0 || creatorID == userID {
		return
	}
	if err := s.notifier.NotifyLiked(ctx, userID, clipID, creatorID); err != nil {
		log.Printf("warning: 点赞通知投递失败 (clip_id=%d): %v", clipID, err)
		observeSideEffect(s.metrics, sideEffectLikeNotify, err)
		return
	}
	observeSideEffect(s.metrics, sideEffectLikeNotify, nil)
}

// recordHotScore 在行为计数真正变化时把对应权重按方向累加进热度分钟桶（S15 hot 场景数据源）：
// 点赞/收藏加正权重，取消点赞/取消收藏加负权重，未发生变化不写入。
// 热度写入失败只记日志与指标：互动已提交，不能因为热度缺失把成功改成失败。
func (s *Service) recordHotScore(ctx context.Context, clipID int64, actionType dominteraction.ActionType, result *dominteraction.ActionResult) {
	if s.hotScores == nil || result == nil || !result.Changed {
		return
	}
	if err := s.hotScores.RecordHotScore(ctx, clipID, hotScoreDelta(actionType, result.Active)); err != nil {
		log.Printf("warning: 互动热度写入失败 (clip_id=%d): %v", clipID, err)
		observeSideEffect(s.metrics, sideEffectHotScore, err)
		return
	}
	observeSideEffect(s.metrics, sideEffectHotScore, nil)
}

// hotScoreDelta 按行为类型与目标状态计算热度增量：点赞 +3/-3、收藏 +4/-4。
func hotScoreDelta(actionType dominteraction.ActionType, active bool) float64 {
	weight := float64(domfeed.HotWeightLike)
	if actionType == dominteraction.ActionSave {
		weight = float64(domfeed.HotWeightSave)
	}
	if !active {
		weight = -weight
	}
	return weight
}

// tryAsyncAction 依次写 Redis、发布事件；前置校验失败或任一步出错都返回 nil 让调用方回退同步落库。
func (s *Service) tryAsyncAction(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, error) {
	// 缓存不校验 clip 是否存在，先在 DB 做一次轻量前置校验，保持不存在时仍返回 404。
	exists, err := s.repo.ExistsPublished(ctx, clipID)
	if err != nil {
		log.Printf("warning: 互动异步链路前置校验失败，回退同步落库 (clip_id=%d): %v", clipID, err)
		return nil, nil
	}
	if !exists {
		// 不存在交给同步路径返回统一 ErrClipNotFound。
		return nil, nil
	}

	result, err := s.stateCache.SetActionState(ctx, userID, clipID, actionType, active)
	if err != nil {
		log.Printf("warning: 互动状态写缓存失败，回退同步落库 (user_id=%d clip_id=%d): %v", userID, clipID, err)
		return nil, nil
	}

	event := ActionChanged{
		UserID:         userID,
		ClipID:         clipID,
		ActionType:     actionType,
		Active:         active,
		IdempotencyKey: idempotencyKey,
		OccurredAt:     time.Now(),
	}
	if err := s.publisher.PublishActionChanged(ctx, event); err != nil {
		log.Printf("warning: action_changed 投递失败，回退同步落库 (user_id=%d clip_id=%d): %v", userID, clipID, err)
		observeSideEffect(s.metrics, sideEffectActionEvent, err)
		// 缓存已写入但事件未投递：状态由后续同键重试或读路径补偿，本次回退同步落库保证接口不丢写。
		return nil, nil
	}
	observeSideEffect(s.metrics, sideEffectActionEvent, nil)
	return result, nil
}

// CreateComment 发表评论：内容校验后写入并返回评论与最新评论数。
func (s *Service) CreateComment(ctx context.Context, userID, clipID int64, content, idempotencyKey string) (*dominteraction.Comment, int64, error) {
	if clipID <= 0 {
		return nil, 0, dominteraction.ErrClipNotFound
	}
	if err := s.enforceWriteLimit(ctx, userID); err != nil {
		return nil, 0, err
	}
	comment, err := dominteraction.NewComment(clipID, userID, content)
	if err != nil {
		return nil, 0, err
	}
	created, total, err := s.repo.CreateComment(ctx, comment, idempotencyKey)
	if err != nil {
		return nil, 0, err
	}
	// 评论通知以评论 ID 为幂等键，重复提交同一评论不会重复打扰创作者；删除评论不撤回历史通知。
	s.notifyComment(ctx, userID, clipID, created)
	return created, total, nil
}

// notifyComment 解析作品创作者并投递评论通知；通知失败只记录日志，不影响评论主链路。
func (s *Service) notifyComment(ctx context.Context, userID, clipID int64, comment *dominteraction.Comment) {
	if s.notifier == nil || s.owners == nil || comment == nil {
		return
	}
	creatorID, err := s.owners.OwnerID(ctx, clipID)
	if err != nil {
		log.Printf("warning: 评论通知解析创作者失败 (clip_id=%d): %v", clipID, err)
		return
	}
	if creatorID <= 0 || creatorID == userID {
		return
	}
	if err := s.notifier.NotifyCommented(ctx, userID, clipID, comment.ID, creatorID, comment.Content); err != nil {
		log.Printf("warning: 评论通知投递失败 (clip_id=%d comment_id=%d): %v", clipID, comment.ID, err)
		observeSideEffect(s.metrics, sideEffectCommentNotify, err)
		return
	}
	observeSideEffect(s.metrics, sideEffectCommentNotify, nil)
}

// DeleteComment 删除评论：作者或 clip 创作者可操作，重复删除幂等。
func (s *Service) DeleteComment(ctx context.Context, userID, commentID int64) (*dominteraction.DeletedComment, error) {
	if commentID <= 0 {
		return nil, dominteraction.ErrCommentNotFound
	}
	if err := s.enforceWriteLimit(ctx, userID); err != nil {
		return nil, err
	}
	return s.repo.DeleteComment(ctx, userID, commentID)
}

// ListComments 按游标分页读取评论；limit 越界返回 ErrInvalidLimit，非正取默认值。
func (s *Service) ListComments(ctx context.Context, clipID int64, cursorStr string, limit int) (*CommentListResult, error) {
	if clipID <= 0 {
		return nil, dominteraction.ErrClipNotFound
	}
	if limit < 0 || limit > dominteraction.MaxListLimit {
		return nil, dominteraction.ErrInvalidLimit
	}
	if limit == 0 {
		limit = dominteraction.DefaultListLimit
	}
	cursor, err := dominteraction.DecodeCommentCursor(cursorStr)
	if err != nil {
		return nil, err
	}

	// 多取一条用于判定是否还有下一页，返回前截断。
	items, err := s.repo.ListComments(ctx, clipID, cursor, limit+1)
	if err != nil {
		return nil, err
	}

	result := &CommentListResult{Items: items}
	if len(items) > limit {
		result.HasMore = true
		result.Items = items[:limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = dominteraction.EncodeCommentCursor(&dominteraction.CommentCursor{
			CreatedAt: last.CreatedAt,
			CommentID: last.ID,
		})
	}
	return result, nil
}
