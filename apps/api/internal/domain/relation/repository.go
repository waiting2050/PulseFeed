// repository.go 定义 relation 领域的仓储端口，应用层只依赖该接口。
package domrelation

import "context"

// Repository 描述关注关系的持久化能力，由 infra 层实现。
type Repository interface {
	// SetFollow 在同一事务内把关注关系置为目标状态并维护 pf_user_stats 双向计数：
	// 状态变化才调整计数，幂等命中仅刷新幂等键；目标用户不存在返回 ErrTargetNotFound。
	// 返回的 FollowResult.Changed 表示本次调用是否发生了状态迁移。
	SetFollow(ctx context.Context, userID, targetUserID int64, follow bool, idempotencyKey string) (*FollowResult, error)
	// ListFollowing 按 (updated_at, target_user_id) 严格递减返回 userID 关注中的目标列表，
	// cursor 为 nil 表示首页；limit 由调用方按页大小 +1 传入以判定是否还有下一页。
	ListFollowing(ctx context.Context, userID int64, cursor *ListCursor, limit int) ([]*ListEntry, error)
	// ListFollowers 按 (updated_at, user_id) 严格递减返回 userID 的粉丝列表，分页语义同上。
	ListFollowers(ctx context.Context, userID int64, cursor *ListCursor, limit int) ([]*ListEntry, error)
}
