package http

import "time"

type feedItemResponse struct {
	VideoID       int64     `json:"video_id"`
	AuthorID      int64     `json:"author_id"`
	Title         string    `json:"title"`
	MediaURL      string    `json:"media_url"`
	CoverURL      string    `json:"cover_url"`
	LikeCount     int64     `json:"like_count"`
	CommentCount  int64     `json:"comment_count"`
	FavoriteCount int64     `json:"favorite_count"`
	PublishedAt   time.Time `json:"published_at"`
}

type feedListResponse struct {
	Items      []feedItemResponse `json:"items"`
	NextCursor string             `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
}
