package domain

import (
	"errors"
	"time"
)

const (
	SceneTimeline  = "timeline"
	SceneHot       = "hot"
	SceneFollowing = "following"
)

var (
	ErrInvalidScene  = errors.New("不支持的 Feed 场景")
	ErrInvalidCursor = errors.New("游标不合法")
	ErrLoginRequired = errors.New("请先登录")
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

// Page 是一页完整的 Feed 结果，缓存时连 has_more 和下一页游标一起保存。
type Page struct {
	Items      []*Item `json:"items"`
	HasMore    bool    `json:"has_more"`
	NextCursor string  `json:"next_cursor"`
}

// ScoreEntry 是热榜里的一条排名记录。
type ScoreEntry struct {
	VideoID int64   `json:"video_id"`
	Score   float64 `json:"score"`
}
