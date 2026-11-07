// dto.go 定义 feed 相关接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpfeed

import "time"

// feedSearchRequest 是 POST /api/feed-search 的请求体，与 GET /api/feed 查询参数同义。
type feedSearchRequest struct {
	Scene  string `json:"scene"`
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

// feedEntryResponse 是 Feed 条目视图响应。
type feedEntryResponse struct {
	ClipID       int64     `json:"clip_id"`
	CreatorID    int64     `json:"creator_id"`
	Title        string    `json:"title"`
	VideoURL     string    `json:"video_url"`
	CoverURL     string    `json:"cover_url"`
	DurationMS   int64     `json:"duration_ms"`
	PublishedAt  time.Time `json:"published_at"`
	LikeCount    int64     `json:"like_count"`
	SaveCount    int64     `json:"save_count"`
	CommentCount int64     `json:"comment_count"`
	PlayCount    int64     `json:"play_count"`
	Liked        bool      `json:"liked"`
	Saved        bool      `json:"saved"`
}

// feedResponse 是 Feed 分页响应。
type feedResponse struct {
	Items      []feedEntryResponse `json:"items"`
	NextCursor string              `json:"next_cursor"`
	HasMore    bool                `json:"has_more"`
}
