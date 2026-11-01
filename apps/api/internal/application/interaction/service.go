// service.go 编排点赞/收藏状态变更与评论增删查用例，只依赖互动领域端口；
// 装配异步流水线后走「Redis 先写 → MQ 投递 → Worker 落库」，失败自动回退同步落库。
package appaction

import (
	"context"
	"log"
	"time"

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

// ActionStateCache 是互动状态的 Redis 先行写入端口，由 infra/cache 实现。
type ActionStateCache interface {
	// SetActionState 更新读者行为状态与分片计数，返回该 clip 的最新计数。
	SetActionState(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool) (*dominteraction.ActionResult, error)
}

// ActionEventPublisher 是 action_changed 事件投递端口，由 infra/mq 实现。
type ActionEventPublisher interface {
	PublishActionChanged(ctx context.Context, event ActionChanged) error
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

// Service 提供互动相关用例。
type Service struct {
	repo       dominteraction.Repository
	stateCache ActionStateCache
	publisher  ActionEventPublisher
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
func (s *Service) setAction(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, error) {
	if clipID <= 0 {
		return nil, dominteraction.ErrClipNotFound
	}
	if s.stateCache != nil && s.publisher != nil {
		if result, ok := s.tryAsyncAction(ctx, userID, clipID, actionType, active, idempotencyKey); ok {
			return result, nil
		}
	}
	return s.repo.SetAction(ctx, userID, clipID, actionType, active, idempotencyKey)
}

// tryAsyncAction 依次写 Redis、发布事件；前置校验失败或任一步出错都返回 ok=false 让调用方回退同步落库。
func (s *Service) tryAsyncAction(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, bool) {
	// 缓存不校验 clip 是否存在，先在 DB 做一次轻量前置校验，保持不存在时仍返回 404。
	exists, err := s.repo.ExistsPublished(ctx, clipID)
	if err != nil {
		log.Printf("warning: 互动异步链路前置校验失败，回退同步落库 (clip_id=%d): %v", clipID, err)
		return nil, false
	}
	if !exists {
		// 不存在交给同步路径返回统一 ErrClipNotFound。
		return nil, false
	}

	result, err := s.stateCache.SetActionState(ctx, userID, clipID, actionType, active)
	if err != nil {
		log.Printf("warning: 互动状态写缓存失败，回退同步落库 (user_id=%d clip_id=%d): %v", userID, clipID, err)
		return nil, false
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
		return nil, false
	}
	return result, true
}

// CreateComment 发表评论：内容校验后写入并返回评论与最新评论数。
func (s *Service) CreateComment(ctx context.Context, userID, clipID int64, content, idempotencyKey string) (*dominteraction.Comment, int64, error) {
	if clipID <= 0 {
		return nil, 0, dominteraction.ErrClipNotFound
	}
	comment, err := dominteraction.NewComment(clipID, userID, content)
	if err != nil {
		return nil, 0, err
	}
	return s.repo.CreateComment(ctx, comment, idempotencyKey)
}

// DeleteComment 删除评论：作者或 clip 创作者可操作，重复删除幂等。
func (s *Service) DeleteComment(ctx context.Context, userID, commentID int64) (*dominteraction.DeletedComment, error) {
	if commentID <= 0 {
		return nil, dominteraction.ErrCommentNotFound
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
