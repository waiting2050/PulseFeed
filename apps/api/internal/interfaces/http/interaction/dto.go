// dto.go 定义互动接口的请求与响应结构，响应统一包裹在 code/message/data 信封内。
package httpaction

import "time"

// likeActionResponse 是点赞/取消点赞的响应视图。
type likeActionResponse struct {
	ClipID     int64  `json:"clip_id"`
	ActionType string `json:"action_type"`
	Active     bool   `json:"active"`
	LikeCount  int64  `json:"like_count"`
}

// saveActionResponse 是收藏/取消收藏的响应视图。
type saveActionResponse struct {
	ClipID     int64  `json:"clip_id"`
	ActionType string `json:"action_type"`
	Active     bool   `json:"active"`
	SaveCount  int64  `json:"save_count"`
}

// createCommentRequest 是发表评论的请求体。
type createCommentRequest struct {
	Content string `json:"content"`
}

// commentResponse 是单条评论视图。
type commentResponse struct {
	ID        int64     `json:"id"`
	ClipID    int64     `json:"clip_id"`
	UserID    int64     `json:"user_id"`
	Nickname  string    `json:"user_nickname"`
	AvatarURL string    `json:"user_avatar_url"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// commentListResponse 是评论分页响应。
type commentListResponse struct {
	Items      []commentResponse `json:"items"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

// createCommentResponse 是发表评论响应：评论视图 + 最新评论数。
type createCommentResponse struct {
	commentResponse
	CommentCount int64 `json:"comment_count"`
}

// deleteCommentResponse 是删除评论响应。
type deleteCommentResponse struct {
	CommentID    int64 `json:"comment_id"`
	Status       int   `json:"status"`
	CommentCount int64 `json:"comment_count"`
}
