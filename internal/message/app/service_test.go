package app

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"pulsefeed/internal/message/domain"
	"pulsefeed/internal/shared/cursor"
	"pulsefeed/internal/shared/event"
)

type memoryRepository struct {
	items  []*domain.Notification
	nextID int64
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{nextID: 1}
}

func (r *memoryRepository) Create(_ context.Context, notification *domain.Notification) error {
	notification.ID = r.nextID
	r.nextID++
	r.items = append(r.items, notification)
	return nil
}

func (r *memoryRepository) List(_ context.Context, userID int64, page *cursor.Page, limit int) ([]*domain.Notification, error) {
	filtered := make([]*domain.Notification, 0, len(r.items))
	for _, item := range r.items {
		if item.UserID != userID {
			continue
		}
		if page != nil && !(item.CreatedAt.Before(page.PublishedAt) ||
			(item.CreatedAt.Equal(page.PublishedAt) && item.ID < page.ID)) {
			continue
		}
		filtered = append(filtered, item)
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID > filtered[j].ID
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (r *memoryRepository) UnreadCount(_ context.Context, userID int64) (int64, error) {
	var count int64
	for _, item := range r.items {
		if item.UserID == userID && !item.IsRead {
			count++
		}
	}
	return count, nil
}

func (r *memoryRepository) MarkAllRead(_ context.Context, userID int64) error {
	for _, item := range r.items {
		if item.UserID == userID {
			item.IsRead = true
		}
	}
	return nil
}

func TestHandleEventCreatesNotification(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo)

	err := service.HandleEvent(context.Background(), event.Event{
		Type:         event.TypeLike,
		ActorID:      1,
		TargetUserID: 2,
		VideoID:      100,
	})
	if err != nil {
		t.Fatalf("handle event: %v", err)
	}
	if len(repo.items) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(repo.items))
	}
	notification := repo.items[0]
	if notification.UserID != 2 || notification.ActorID != 1 || notification.Type != "like" || notification.VideoID != 100 {
		t.Fatalf("unexpected notification: %+v", notification)
	}
	if notification.IsRead {
		t.Fatal("new notification should be unread")
	}
}

func TestHandleEventSkipsSelfAction(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo)

	if err := service.HandleEvent(context.Background(), event.Event{
		Type:         event.TypeComment,
		ActorID:      2,
		TargetUserID: 2,
		VideoID:      100,
	}); err != nil {
		t.Fatalf("handle event: %v", err)
	}
	if len(repo.items) != 0 {
		t.Fatalf("expected no notification, got %d", len(repo.items))
	}
}

func TestHandleEventTruncatesCommentExcerpt(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo)

	content := strings.Repeat("字", maxExcerptRunes+20)
	if err := service.HandleEvent(context.Background(), event.Event{
		Type:         event.TypeComment,
		ActorID:      1,
		TargetUserID: 2,
		VideoID:      100,
		CommentID:    9,
		Content:      content,
	}); err != nil {
		t.Fatalf("handle event: %v", err)
	}
	if len(repo.items) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(repo.items))
	}
	notification := repo.items[0]
	if utf8.RuneCountInString(notification.Content) != maxExcerptRunes {
		t.Fatalf("expected %d runes, got %d", maxExcerptRunes, utf8.RuneCountInString(notification.Content))
	}
	if notification.CommentID != 9 {
		t.Fatalf("unexpected comment id: %d", notification.CommentID)
	}
}

func TestListPagination(t *testing.T) {
	repo := newMemoryRepository()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := repo.Create(context.Background(), &domain.Notification{
			UserID:    1,
			ActorID:   int64(10 + i),
			Type:      "like",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("seed notification: %v", err)
		}
	}
	service := NewService(repo)

	first, err := service.List(context.Background(), 1, "", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	if first.Items[0].ActorID != 12 || first.Items[1].ActorID != 11 {
		t.Fatalf("unexpected order: %+v", first.Items)
	}

	second, err := service.List(context.Background(), 1, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("next page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ActorID != 10 {
		t.Fatalf("unexpected second page: %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("expected last page, got %+v", second)
	}
}

func TestListRejectsInvalidCursor(t *testing.T) {
	service := NewService(newMemoryRepository())

	if _, err := service.List(context.Background(), 1, "not-base64", 10); err != domain.ErrInvalidCursor {
		t.Fatalf("expected invalid cursor, got %v", err)
	}
}

func TestUnreadCountAndMarkAllRead(t *testing.T) {
	repo := newMemoryRepository()
	for i := 0; i < 2; i++ {
		if err := repo.Create(context.Background(), &domain.Notification{UserID: 1, Type: "like", CreatedAt: time.Now()}); err != nil {
			t.Fatalf("seed notification: %v", err)
		}
	}
	service := NewService(repo)

	count, err := service.UnreadCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("unread count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 unread, got %d", count)
	}

	if err := service.MarkAllRead(context.Background(), 1); err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	count, err = service.UnreadCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("unread count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unread, got %d", count)
	}
}
