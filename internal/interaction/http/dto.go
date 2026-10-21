package http

import (
	"time"

	"pulsefeed/internal/interaction/domain"
)

type createCommentRequest struct {
	Content string `json:"content"`
}

type actionResponse struct {
	VideoID int64  `json:"video_id"`
	Action  string `json:"action"`
	Active  bool   `json:"active"`
}

type commentResponse struct {
	ID        int64     `json:"id"`
	VideoID   int64     `json:"video_id"`
	UserID    int64     `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type commentListResponse struct {
	Items      []commentResponse `json:"items"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

func toCommentResponse(comment *domain.Comment) *commentResponse {
	return &commentResponse{
		ID:        comment.ID,
		VideoID:   comment.VideoID,
		UserID:    comment.UserID,
		Content:   comment.Content,
		CreatedAt: comment.CreatedAt,
	}
}
