// dto.go 定义 playback 相关接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpplayback

// configResponse 是播放配置的读取/写入响应。
type configResponse struct {
	PreloadCount  int     `json:"preload_count"`
	QoSSampleRate float64 `json:"qos_sample_rate"`
}

// configUpdateRequest 是 PUT /api/playback-config 的请求体，字段全可选。
type configUpdateRequest struct {
	PreloadCount  *int     `json:"preload_count,omitempty"`
	QoSSampleRate *float64 `json:"qos_sample_rate,omitempty"`
}

// preloadResponse 是预加载 clip 列表的响应，仅返回 clip_id。
type preloadResponse struct {
	ClipIDs []int64 `json:"clip_ids"`
}

// qosReportRequest 是 QoS 上报请求体；event_id 必填（幂等）。
type qosReportRequest struct {
	ClipID     int64  `json:"clip_id"`
	EventID    string `json:"event_id"`
	LatencyMS  int    `json:"latency_ms"`
	StallCount int    `json:"stall_count"`
	Completed  bool   `json:"completed"`
	ClientInfo string `json:"client_info,omitempty"`
}

// qosReportResponse 是 QoS 上报响应；inserted=false 表示命中幂等。
type qosReportResponse struct {
	Inserted bool `json:"inserted"`
}
