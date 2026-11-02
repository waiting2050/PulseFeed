// dto.go 定义 internal 推荐/曝光接口的请求与响应结构。
package httprecommendation

import "time"

// candidateRequest 是 POST /internal/recommendation-candidates 的请求体。
type candidateRequest struct {
	ViewerID int64 `json:"viewer_id"`
	Limit    int   `json:"limit"`
}

// candidateResponse 是候选召回的一条记录。
type candidateResponse struct {
	ClipID       int64     `json:"clip_id"`
	CreatorID    int64     `json:"creator_id"`
	PublishedAt  time.Time `json:"published_at"`
	HotScore     float64   `json:"hot_score"`
	HasEmbedding bool      `json:"has_embedding"`
}

// candidateListResponse 是候选召回列表响应。
type candidateListResponse struct {
	ViewerID int64               `json:"viewer_id"`
	Items    []candidateResponse `json:"items"`
}

// impressionDecisionResponse 是某 clip 是否可再次展示的内部决策响应。
type impressionDecisionResponse struct {
	ViewerID     int64      `json:"viewer_id"`
	ClipID       int64      `json:"clip_id"`
	ShouldShow   bool       `json:"should_show"`
	Reason       string     `json:"reason"`
	LastPlayedAt *time.Time `json:"last_played_at"`
}

// impressionResponse 是一条按日曝光聚合记录。
type impressionResponse struct {
	ViewerID       int64     `json:"viewer_id"`
	ClipID         int64     `json:"clip_id"`
	Day            string    `json:"day"`
	PlayCount      int64     `json:"play_count"`
	CompletedCount int64     `json:"completed_count"`
	FirstPlayedAt  time.Time `json:"first_played_at"`
	LastPlayedAt   time.Time `json:"last_played_at"`
}

// impressionListResponse 是曝光记录列表响应。
type impressionListResponse struct {
	ViewerID int64                `json:"viewer_id"`
	Days     int                  `json:"days"`
	Items    []impressionResponse `json:"items"`
}
