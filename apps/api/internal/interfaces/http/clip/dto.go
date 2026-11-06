// dto.go 定义 clip 相关接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpclip

import "time"

// publishRequest 是发布 clip 的请求体，幂等键走 Idempotency-Key 请求头。
// duration_ms 由上传接口探测回传，可选；缺失或非法时服务端按 0 处理。
type publishRequest struct {
	Title      string `json:"title"`
	VideoURL   string `json:"video_url"`
	CoverURL   string `json:"cover_url"`
	DurationMS int64  `json:"duration_ms"`
}

// clipResponse 是 clip 视图响应；duration_ms 为 0 表示未探测到时长。
type clipResponse struct {
	ClipID      int64     `json:"clip_id"`
	CreatorID   int64     `json:"creator_id"`
	Title       string    `json:"title"`
	VideoURL    string    `json:"video_url"`
	CoverURL    string    `json:"cover_url"`
	DurationMS  int64     `json:"duration_ms"`
	PublishedAt time.Time `json:"published_at"`
}

// clipListResponse 是创作者 clip 列表的分页响应。
type clipListResponse struct {
	Items      []clipResponse `json:"items"`
	NextCursor string         `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}
