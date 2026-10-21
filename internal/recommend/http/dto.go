package http

import "time"

type recommendItemResponse struct {
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

type recommendResponse struct {
	Items []recommendItemResponse `json:"items"`
}
