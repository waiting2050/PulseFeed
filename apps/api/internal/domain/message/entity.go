// entity.go 定义 message 领域的实体与值对象：站内消息、种类枚举与读状态。
package dommessage

import "time"

// Kind 是站内消息的种类枚举，按事件源区分。
type Kind string

const (
	// KindFollowed 表示被关注者收到的关注通知（S16 首个接入）。
	KindFollowed Kind = "followed"
)

// MaxLimit 是消息列表分页的上限，为避免滥用而固定。
const MaxLimit = 100

// DefaultLimit 是列表分页的默认值。
const DefaultLimit = 20

// Message 是一条站内消息：接收者、发送者、事件类型与幂等事件 ID；
// 未读时 ReadAt 为零值（NULL）。
type Message struct {
	ID         int64
	ReceiverID int64
	ActorID    int64
	Kind       Kind
	TargetID   int64
	EventID    string
	ReadAt     time.Time
	CreatedAt  time.Time
}

// IsRead 报告该消息是否已读。
func (m *Message) IsRead() bool {
	return !m.ReadAt.IsZero()
}

// ListCursor 是消息列表的游标：按 (created_at, id) 严格递减续翻。
type ListCursor struct {
	CreatedAt time.Time
	ID        int64
}
