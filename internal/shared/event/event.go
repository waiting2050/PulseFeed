package event

import (
	"context"
	"sync"
)

// Type 标识互动事件类型，消息模块据此生成通知。
type Type string

const (
	TypeLike     Type = "like"
	TypeFavorite Type = "favorite"
	TypeComment  Type = "comment"
	TypeFollow   Type = "follow"
)

type Event struct {
	Type         Type
	ActorID      int64
	TargetUserID int64
	VideoID      int64
	CommentID    int64
	Content      string
}

// Handler 处理事件，失败由发布方记录日志，不影响主流程。
type Handler func(ctx context.Context, evt Event)

// Bus 是进程内同步事件总线，订阅者按注册顺序依次执行。
type Bus struct {
	mu       sync.RWMutex
	handlers []Handler
}

func NewBus() *Bus {
	return &Bus{}
}

func (b *Bus) Subscribe(handler Handler) {
	if handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, handler)
}

func (b *Bus) Publish(ctx context.Context, evt Event) {
	b.mu.RLock()
	handlers := make([]Handler, len(b.handlers))
	copy(handlers, b.handlers)
	b.mu.RUnlock()

	for _, handler := range handlers {
		handler(ctx, evt)
	}
}
