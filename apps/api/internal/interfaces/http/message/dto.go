// dto.go 定义 message 相关接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpmsg

import "time"

// messageResponse 是单条站内消息的传输结构。
type messageResponse struct {
	ID        int64      `json:"id"`
	ActorID   int64      `json:"actor_id"`
	Kind      string     `json:"kind"`
	TargetID  int64      `json:"target_id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	EventID   string     `json:"event_id"`
	Read      bool       `json:"read"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// markReadRequest 是按条已读请求体；ids 缺省或为空时退化为全部已读。
type markReadRequest struct {
	IDs []int64 `json:"ids"`
}

// listResponse 是消息列表分页响应。
type listResponse struct {
	Items      []messageResponse `json:"items"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

// unreadResponse 是未读数计数响应。
type unreadResponse struct {
	Unread int64 `json:"unread"`
}

// markReadResponse 是标记已读回执：marked 为实际更新行数，all 表示本次为全部已读。
type markReadResponse struct {
	Marked int64 `json:"marked"`
	All    bool  `json:"all"`
}

// createRequest 是 internal 路由主动投递消息的请求体。
type createRequest struct {
	ReceiverID int64  `json:"receiver_id"`
	ActorID    int64  `json:"actor_id"`
	TargetID   int64  `json:"target_id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	EventID    string `json:"event_id"`
}
