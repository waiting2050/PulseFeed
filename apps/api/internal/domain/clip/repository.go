// repository.go 定义 clip 领域的仓储端口，应用层只依赖该接口。
package domclip

import "context"

// OutboxEvent 是与业务数据同事务落库、由 relay 异步投递的事件记录。
// 重复投递时下游按 EventID 识别同一事件（配合各自的幂等写），因此 relay 可以安全重试。
type OutboxEvent struct {
	// EventID 是全局唯一的事件 ID，写入 AMQP MessageId 与事件载荷。
	EventID string
	// Topic 是逻辑事件类型，relay 据此解析交换机与路由键。
	Topic string
	// Payload 是已序列化的 JSON 事件载荷。
	Payload []byte
}

// EventBuilder 在 clip 自增 ID 生成后构造 outbox 事件，保证载荷携带最终 clip_id。
type EventBuilder func(c *Clip) ([]OutboxEvent, error)

// Repository 描述 clip 的持久化能力，由 infra 层实现。
type Repository interface {
	// Create 在同一事务内写入 clip 与计数行；幂等键唯一索引冲突返回 ErrClipExists，并回写自增 ID。
	Create(ctx context.Context, c *Clip) error
	// CreateWithEvents 在同一事务内写入 clip、计数行与 outbox 事件；
	// 任一写入失败整体回滚，避免“业务落库但事件丢失”或“事件存在但业务回滚”。
	CreateWithEvents(ctx context.Context, c *Clip, build EventBuilder) error
	// GetByID 按主键读取 clip；不存在时返回 ErrClipNotFound（含已删除，由应用层判定可见性）。
	GetByID(ctx context.Context, id int64) (*Clip, error)
	// GetByIdempotencyKey 按 (creatorID, idempotencyKey) 查重，幂等发布流程使用；不存在时返回 ErrClipNotFound。
	GetByIdempotencyKey(ctx context.Context, creatorID int64, idempotencyKey string) (*Clip, error)
	// ListByCreator 按 (published_at, id) 严格递减返回创作者可见 clip，cursor 为 nil 表示首页。
	ListByCreator(ctx context.Context, creatorID int64, cursor *Cursor, limit int) ([]*Clip, error)
	// Delete 将 clip 置为已删除；不存在返回 ErrClipNotFound，已删除重复调用幂等返回 nil。
	Delete(ctx context.Context, id int64, idempotencyKey string) error
}
