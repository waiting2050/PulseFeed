package http

import "time"

type notificationResponse struct {
	ID            int64     `json:"id"`
	Type          string    `json:"type"`
	ActorID       int64     `json:"actor_id"`
	ActorNickname string    `json:"actor_nickname"`
	ActorAvatar   string    `json:"actor_avatar"`
	VideoID       int64     `json:"video_id,omitempty"`
	CommentID     int64     `json:"comment_id,omitempty"`
	Content       string    `json:"content,omitempty"`
	IsRead        bool      `json:"is_read"`
	CreatedAt     time.Time `json:"created_at"`
}

type notificationListResponse struct {
	Items      []notificationResponse `json:"items"`
	NextCursor string                 `json:"next_cursor"`
	HasMore    bool                   `json:"has_more"`
}

type unreadCountResponse struct {
	Count int64 `json:"count"`
}
