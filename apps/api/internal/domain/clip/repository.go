// repository.go 定义 clip 领域的仓储端口，应用层只依赖该接口。
package domclip

import "context"

// Repository 描述 clip 的持久化能力，由 infra 层实现。
type Repository interface {
	// Create 在同一事务内写入 clip 与计数行；幂等键唯一索引冲突返回 ErrClipExists，并回写自增 ID。
	Create(ctx context.Context, c *Clip) error
	// GetByID 按主键读取 clip；不存在时返回 ErrClipNotFound（含已删除，由应用层判定可见性）。
	GetByID(ctx context.Context, id int64) (*Clip, error)
	// GetByIdempotencyKey 按 (creatorID, idempotencyKey) 查重，幂等发布流程使用；不存在时返回 ErrClipNotFound。
	GetByIdempotencyKey(ctx context.Context, creatorID int64, idempotencyKey string) (*Clip, error)
	// ListByCreator 按 (published_at, id) 严格递减返回创作者可见 clip，cursor 为 nil 表示首页。
	ListByCreator(ctx context.Context, creatorID int64, cursor *Cursor, limit int) ([]*Clip, error)
	// Delete 将 clip 置为已删除；不存在返回 ErrClipNotFound，已删除重复调用幂等返回 nil。
	Delete(ctx context.Context, id int64, idempotencyKey string) error
}
