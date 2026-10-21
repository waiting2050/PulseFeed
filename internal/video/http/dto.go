package http

import "time"

type createVideoRequest struct {
	Title    string `json:"title" binding:"required"`
	MediaURL string `json:"media_url" binding:"required"`
	CoverURL string `json:"cover_url"`
}

type videoResponse struct {
	ID          int64     `json:"id"`
	AuthorID    int64     `json:"author_id"`
	Title       string    `json:"title"`
	MediaURL    string    `json:"media_url"`
	CoverURL    string    `json:"cover_url"`
	PublishedAt time.Time `json:"published_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type videoListResponse struct {
	Items      []videoResponse `json:"items"`
	NextCursor string          `json:"next_cursor"`
	HasMore    bool            `json:"has_more"`
}
