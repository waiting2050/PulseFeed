package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"pulsefeed/internal/message/domain"
	"pulsefeed/internal/shared/cursor"
	"pulsefeed/internal/shared/event"
)

const (
	defaultPageSize = 20
	maxPageSize     = 50
	maxExcerptRunes = 100
)

type ListResult struct {
	Items      []*domain.Notification
	NextCursor string
	HasMore    bool
}

type Service struct {
	repo domain.Repository
}

func NewService(repo domain.Repository) *Service {
	return &Service{repo: repo}
}

// HandleEvent 把互动事件转换为通知；自己操作自己不产生通知。
func (s *Service) HandleEvent(ctx context.Context, evt event.Event) error {
	if evt.ActorID <= 0 || evt.TargetUserID <= 0 || evt.ActorID == evt.TargetUserID {
		return nil
	}

	notification := &domain.Notification{
		UserID:    evt.TargetUserID,
		ActorID:   evt.ActorID,
		Type:      string(evt.Type),
		VideoID:   evt.VideoID,
		CommentID: evt.CommentID,
		Content:   excerpt(evt.Content),
		CreatedAt: time.Now().UTC(),
	}
	return s.repo.Create(ctx, notification)
}

func (s *Service) List(ctx context.Context, userID int64, cursorValue string, limit int) (*ListResult, error) {
	limit = clampLimit(limit)

	var page *cursor.Page
	if cursorValue != "" {
		decoded, err := cursor.Decode(cursorValue)
		if err != nil {
			return nil, domain.ErrInvalidCursor
		}
		page = decoded
	}

	notifications, err := s.repo.List(ctx, userID, page, limit+1)
	if err != nil {
		return nil, err
	}

	hasMore := len(notifications) > limit
	if hasMore {
		notifications = notifications[:limit]
	}
	if notifications == nil {
		notifications = []*domain.Notification{}
	}

	result := &ListResult{Items: notifications, HasMore: hasMore}
	if hasMore && len(notifications) > 0 {
		last := notifications[len(notifications)-1]
		result.NextCursor = cursor.Encode(last.CreatedAt, last.ID)
	}
	return result, nil
}

func (s *Service) UnreadCount(ctx context.Context, userID int64) (int64, error) {
	return s.repo.UnreadCount(ctx, userID)
}

func (s *Service) MarkAllRead(ctx context.Context, userID int64) error {
	return s.repo.MarkAllRead(ctx, userID)
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultPageSize
	}
	if limit > maxPageSize {
		return maxPageSize
	}
	return limit
}

func excerpt(content string) string {
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) <= maxExcerptRunes {
		return content
	}
	runes := []rune(content)
	return string(runes[:maxExcerptRunes])
}
