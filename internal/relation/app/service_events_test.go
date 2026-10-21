package app

import (
	"context"
	"testing"

	"pulsefeed/internal/shared/event"
)

type memoryPublisher struct {
	events []event.Event
}

func (p *memoryPublisher) Publish(_ context.Context, evt event.Event) {
	p.events = append(p.events, evt)
}

func TestFollowPublishesNotificationOnlyWhenCreated(t *testing.T) {
	repo := newMemoryRepository()
	publisher := &memoryPublisher{}
	service := NewService(repo, publisher)
	ctx := context.Background()

	if err := service.Follow(ctx, 1, 2); err != nil {
		t.Fatalf("follow: %v", err)
	}
	if err := service.Follow(ctx, 1, 2); err != nil {
		t.Fatalf("duplicate follow: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected 1 event, got %+v", publisher.events)
	}
	evt := publisher.events[0]
	if evt.Type != event.TypeFollow || evt.ActorID != 1 || evt.TargetUserID != 2 {
		t.Fatalf("unexpected event: %+v", evt)
	}

	if err := service.Unfollow(ctx, 1, 2); err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if err := service.Unfollow(ctx, 1, 2); err != nil {
		t.Fatalf("duplicate unfollow: %v", err)
	}
	if err := service.Follow(ctx, 1, 2); err != nil {
		t.Fatalf("re-follow: %v", err)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("expected re-follow to publish again, got %+v", publisher.events)
	}
}
