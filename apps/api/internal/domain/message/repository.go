// repository.go 定义 message 领域的仓储端口，应用层只依赖该接口。
package dommessage

import "context"

// Repository 描述站内消息的持久化能力，由 infra 层实现。
type Repository interface {
	// Create 插入一条消息；唯一索引 uk_event_id 已存在时静默忽略（重复事件幂等），
	// 不向上返回错误，调用方通过新 ID>0 或返回 nil 行判定是否真的新增。
	Create(ctx context.Context, msg *Message) (inserted bool, err error)
	// ListPage 按 (created_at, id) 严格递减返回 receiverID 可见消息；
	// cursor 为 nil 表示首页；limit 由调用方按页大小 +1 传入以判定 has_more。
	ListPage(ctx context.Context, receiverID int64, cursor *ListCursor, limit int) ([]*Message, error)
	// CountUnread 返回 receiverID 的未读消息数。
	CountUnread(ctx context.Context, receiverID int64) (int64, error)
	// MarkAllRead 把 receiverID 的所有未读消息一次性标记已读。
	MarkAllRead(ctx context.Context, receiverID int64) error
	// MarkReadByIDs 仅把 receiverID 名下、给定 ID 列表中的未读消息标记已读，返回本次实际更新行数；
	// 不属于该接收者的 ID 会被忽略，重复标记幂等。
	MarkReadByIDs(ctx context.Context, receiverID int64, ids []int64) (int64, error)
}
