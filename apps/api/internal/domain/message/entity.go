// entity.go 定义 message 领域的实体与值对象：站内消息、种类枚举与读状态。
package dommessage

import "time"

// Kind 是站内消息的种类枚举，按事件源区分。
type Kind string

const (
	// KindLike 表示作品被点赞的通知。
	KindLike Kind = "LIKE"
	// KindComment 表示作品被评论的通知。
	KindComment Kind = "COMMENT"
	// KindFollow 表示被关注的通知。
	KindFollow Kind = "FOLLOW"
	// KindSystem 表示系统/运维主动投递的通知。
	KindSystem Kind = "SYSTEM"

	// KindFollowed 是旧版关注通知的落库值，读取时统一归一化为 KindFollow，保证旧数据可读。
	KindFollowed Kind = "followed"
)

// NormalizeKind 把旧版消息种类映射为当前枚举，未知值原样返回。
func NormalizeKind(k Kind) Kind {
	if k == KindFollowed {
		return KindFollow
	}
	return k
}

// DefaultTitle 返回各类消息在未显式提供标题时的兜底标题。
func DefaultTitle(k Kind) string {
	switch NormalizeKind(k) {
	case KindLike:
		return "收到新的点赞"
	case KindComment:
		return "收到新的评论"
	case KindFollow:
		return "新的关注者"
	case KindSystem:
		return "系统通知"
	default:
		return "新消息"
	}
}

// MaxLimit 是消息列表分页的上限，为避免滥用而固定。
const MaxLimit = 100

// DefaultLimit 是列表分页的默认值。
const DefaultLimit = 20

// Message 是一条站内消息：接收者、发送者、事件类型、标题正文与幂等事件 ID；
// 未读时 ReadAt 为零值（NULL），对外 IsRead 由 ReadAt 派生而非单独落布尔列。
type Message struct {
	ID         int64
	ReceiverID int64
	ActorID    int64
	Kind       Kind
	TargetID   int64
	Title      string
	Content    string
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
