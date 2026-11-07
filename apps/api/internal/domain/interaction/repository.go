// repository.go 定义互动领域的仓储端口，应用层只依赖该接口。
package dominteraction

import "context"

// Repository 描述点赞/收藏与评论的持久化能力，由 infra 层实现。
type Repository interface {
	// SetAction 在同一事务内把行为置为目标状态并维护计数：
	// 状态变化才调整 pf_clip_stats，幂等命中仅刷新幂等键；返回最新计数。
	// clip 不存在或非发布态返回 ErrClipNotFound。
	SetAction(ctx context.Context, userID, clipID int64, actionType ActionType, active bool, idempotencyKey string) (*ActionResult, error)
	// ExistsPublished 报告 clip 是否存在且处于发布态，供异步链路在写缓存前做前置校验。
	ExistsPublished(ctx context.Context, clipID int64) (bool, error)
	// CreateComment 在同一事务内写入评论并递增计数；
	// 同 (userID, 幂等键) 命中时直接返回既有评论且不重复计数。
	CreateComment(ctx context.Context, comment *Comment, idempotencyKey string) (*Comment, int64, error)
	// DeleteComment 软删除评论并递减计数；重复删除幂等返回当前结果。
	DeleteComment(ctx context.Context, requesterID, commentID int64) (*DeletedComment, error)
	// ListComments 按 (created_at, id) 严格递减返回可见评论，cursor 为 nil 表示首页。
	ListComments(ctx context.Context, clipID int64, cursor *CommentCursor, limit int) ([]*Comment, error)
}
