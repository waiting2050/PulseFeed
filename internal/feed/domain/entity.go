package domain

import (
	"errors"
	"time"
)

const SceneTimeline = "timeline"

var (
	ErrInvalidScene  = errors.New("不支持的 Feed 场景")
	ErrInvalidCursor = errors.New("游标不合法")
)

// Item 是 Feed 页里的一张视频卡片，字段直接对应组装结果。
type Item struct {
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

// Page 是一页完整的 Feed 结果，缓存时连 has_more 一起保存，避免回源判断。
type Page struct {
	Items   []*Item `json:"items"`
	HasMore bool    `json:"has_more"`
}
