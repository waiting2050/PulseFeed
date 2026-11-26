// dto.go 定义展示接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpimpression

// recordPlayEventRequest 是展示/播放事件上报请求体。
// event_type、scene、request_id、watch_ms 为新增字段；play_duration_ms 保留为旧客户端兼容别名。
type recordPlayEventRequest struct {
	EventID        string `json:"event_id"`
	ClipID         int64  `json:"clip_id"`
	EventType      string `json:"event_type"`
	Scene          string `json:"scene"`
	RequestID      string `json:"request_id"`
	WatchMs        int64  `json:"watch_ms"`
	PlayDurationMs int64  `json:"play_duration_ms"`
	Completed      bool   `json:"completed"`
	Impression     bool   `json:"impression"`
}

// recordPlayEventResponse 是展示/播放事件上报响应，回传事件 ID 便于客户端核对幂等。
type recordPlayEventResponse struct {
	EventID string `json:"event_id"`
}
