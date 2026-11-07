// dto.go 定义曝光接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpimpression

// recordPlayEventRequest 是播放事件上报请求体。
type recordPlayEventRequest struct {
	EventID        string `json:"event_id"`
	ClipID         int64  `json:"clip_id"`
	PlayDurationMs int64  `json:"play_duration_ms"`
	Completed      bool   `json:"completed"`
	Impression     bool   `json:"impression"`
}

// recordPlayEventResponse 是播放事件上报响应，回传事件 ID 便于客户端核对幂等。
type recordPlayEventResponse struct {
	EventID string `json:"event_id"`
}
