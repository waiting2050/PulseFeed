package app

import (
	"context"
	"testing"

	"pulsefeed/internal/interaction/domain"
	"pulsefeed/internal/shared/event"
)

func (r *memoryRepository) VideoAuthor(_ context.Context, _ int64) (int64, error) {
	if !r.videoExists {
		return 0, domain.ErrVideoNotFound
	}
	return r.videoAuthor, nil
}

type memoryPublisher struct {
	events []event.Event
}

func (p *memoryPublisher) Publish(_ context.Context, evt event.Event) {
	p.events = append(p.events, evt)
}

func TestLikePublishesNotificationOnlyWhenChanged(t *testing.T) {
	repo := newMemoryRepository()
	publisher := &memoryPublisher{}
	service := NewService(repo, &memoryHotStore{}, publisher)
	ctx := context.Background()

	if err := service.Like(ctx, 1, 100); err != nil {
		t.Fatalf("like: %v", err)
	}
	if err := service.Like(ctx, 1, 100); err != nil {
		t.Fatalf("duplicate like: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected 1 event, got %+v", publisher.events)
	}
	evt := publisher.events[0]
	if evt.Type != event.TypeLike || evt.ActorID != 1 || evt.TargetUserID != repo.videoAuthor || evt.VideoID != 100 {
		t.Fatalf("unexpected event: %+v", evt)
	}

	if err := service.Unlike(ctx, 1, 100); err != nil {
		t.Fatalf("unlike: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("unlike should not publish event, got %+v", publisher.events)
	}

	if err := service.Favorite(ctx, 1, 100); err != nil {
		t.Fatalf("favorite: %v", err)
	}
	if len(publisher.events) != 2 || publisher.events[1].Type != event.TypeFavorite {
		t.Fatalf("unexpected events: %+v", publisher.events)
	}
}

func TestCommentPublishesNotification(t *testing.T) {
	repo := newMemoryRepository()
	publisher := &memoryPublisher{}
	service := NewService(repo, nil, publisher)

	comment, err := service.CreateComment(context.Background(), 1, 100, "  你好  ")
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected 1 event, got %+v", publisher.events)
	}
	evt := publisher.events[0]
	if evt.Type != event.TypeComment || evt.ActorID != 1 || evt.TargetUserID != repo.videoAuthor {
		t.Fatalf("unexpected event: %+v", evt)
	}
	if evt.CommentID != comment.ID || evt.Content != "你好" || evt.VideoID != 100 {
		t.Fatalf("unexpected comment event: %+v", evt)
	}
}
