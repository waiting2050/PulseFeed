package app

import (
	"context"
	"testing"
	"time"

	"pulsefeed/internal/feed/domain"
	"pulsefeed/internal/shared/cursor"
)

type memoryRepository struct {
	items []*domain.Item
	calls int
	err   error
}

func (r *memoryRepository) ListTimeline(_ context.Context, page *cursor.Page, limit int) ([]*domain.Item, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	if len(r.items) > limit {
		return r.items[:limit], nil
	}
	return r.items, nil
}

func (r *memoryRepository) ListByIDs(_ context.Context, ids []int64) ([]*domain.Item, error) {
	if r.err != nil {
		return nil, r.err
	}
	wanted := make(map[int64]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	cards := make([]*domain.Item, 0, len(ids))
	for _, item := range r.items {
		if wanted[item.VideoID] {
			cards = append(cards, item)
		}
	}
	return cards, nil
}

type memoryCache struct {
	pages map[string]*domain.Page
}

func newMemoryCache() *memoryCache {
	return &memoryCache{pages: map[string]*domain.Page{}}
}

func (c *memoryCache) Get(_ context.Context, key string) (*domain.Page, bool) {
	page, ok := c.pages[key]
	return page, ok
}

func (c *memoryCache) Set(_ context.Context, key string, page *domain.Page, _ time.Duration) {
	c.pages[key] = page
}

func newItems(n int) []*domain.Item {
	items := make([]*domain.Item, 0, n)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		items = append(items, &domain.Item{
			VideoID:     int64(100 - i),
			AuthorID:    1,
			Title:       "video",
			PublishedAt: base.Add(-time.Duration(i) * time.Minute),
		})
	}
	return items
}

func TestListTimelineDefaults(t *testing.T) {
	repo := &memoryRepository{items: newItems(3)}
	service := NewService(repo, nil, nil)

	result, err := service.List(context.Background(), "", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result.Items))
	}
	if result.HasMore {
		t.Fatal("expected no more pages")
	}
	if result.NextCursor != "" {
		t.Fatalf("expected empty cursor, got %q", result.NextCursor)
	}
}

func TestListTimelinePagination(t *testing.T) {
	repo := &memoryRepository{items: newItems(3)}
	service := NewService(repo, nil, nil)

	result, err := service.List(context.Background(), domain.SceneTimeline, "", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
	if !result.HasMore {
		t.Fatal("expected has_more")
	}

	page, err := cursor.Decode(result.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	last := result.Items[1]
	if page.ID != last.VideoID || !page.PublishedAt.Equal(last.PublishedAt) {
		t.Fatalf("cursor mismatch: got id=%d at=%s", page.ID, page.PublishedAt)
	}
}

func TestListTimelineRejectsInvalidInput(t *testing.T) {
	service := NewService(&memoryRepository{items: newItems(1)}, nil, nil)

	if _, err := service.List(context.Background(), "unknown", "", 10); err != domain.ErrInvalidScene {
		t.Fatalf("expected invalid scene, got %v", err)
	}
	if _, err := service.List(context.Background(), domain.SceneTimeline, "not-base64", 10); err != domain.ErrInvalidCursor {
		t.Fatalf("expected invalid cursor, got %v", err)
	}
}

func TestListTimelineUsesPageCache(t *testing.T) {
	repo := &memoryRepository{items: newItems(2)}
	cache := newMemoryCache()
	service := NewService(repo, cache, nil)

	first, err := service.List(context.Background(), domain.SceneTimeline, "", 10)
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	second, err := service.List(context.Background(), domain.SceneTimeline, "", 10)
	if err != nil {
		t.Fatalf("second list: %v", err)
	}
	if repo.calls != 1 {
		t.Fatalf("expected 1 repository call, got %d", repo.calls)
	}
	if len(second.Items) != len(first.Items) {
		t.Fatalf("cache result mismatch: %d vs %d", len(second.Items), len(first.Items))
	}
	if second.Items[0].VideoID != first.Items[0].VideoID {
		t.Fatalf("cache returned different page")
	}
}

func TestListTimelineCacheKeySeparatesPages(t *testing.T) {
	repo := &memoryRepository{items: newItems(3)}
	cache := newMemoryCache()
	service := NewService(repo, cache, nil)

	first, err := service.List(context.Background(), domain.SceneTimeline, "", 2)
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	if _, err := service.List(context.Background(), domain.SceneTimeline, first.NextCursor, 2); err != nil {
		t.Fatalf("next page: %v", err)
	}
	if repo.calls != 2 {
		t.Fatalf("expected 2 repository calls, got %d", repo.calls)
	}
}

type memoryHotStore struct {
	entries []domain.ScoreEntry
	calls   int
}

func (h *memoryHotStore) TopScores(_ context.Context, _ time.Duration, _ int) ([]domain.ScoreEntry, error) {
	h.calls++
	return h.entries, nil
}

func (h *memoryHotStore) AddScore(_ context.Context, _ int64, _ float64) error {
	return nil
}

func TestListHotOrdersCardsByScore(t *testing.T) {
	repo := &memoryRepository{items: newItems(3)}
	hot := &memoryHotStore{entries: []domain.ScoreEntry{
		{VideoID: 98, Score: 90},
		{VideoID: 100, Score: 50},
		{VideoID: 99, Score: 10},
	}}
	service := NewService(repo, nil, hot)

	result, err := service.List(context.Background(), domain.SceneHot, "", 2)
	if err != nil {
		t.Fatalf("hot list: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
	if result.Items[0].VideoID != 98 || result.Items[1].VideoID != 100 {
		t.Fatalf("unexpected order: %d, %d", result.Items[0].VideoID, result.Items[1].VideoID)
	}
	if !result.HasMore {
		t.Fatal("expected has_more")
	}

	rank, err := cursor.DecodeRank(result.NextCursor)
	if err != nil {
		t.Fatalf("decode rank: %v", err)
	}
	if rank.Score != 50 || rank.ID != 100 {
		t.Fatalf("unexpected rank cursor: %+v", rank)
	}

	next, err := service.List(context.Background(), domain.SceneHot, result.NextCursor, 2)
	if err != nil {
		t.Fatalf("hot next page: %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].VideoID != 99 {
		t.Fatalf("unexpected next page: %+v", next.Items)
	}
	if next.HasMore || next.NextCursor != "" {
		t.Fatalf("expected last page, got has_more=%v cursor=%q", next.HasMore, next.NextCursor)
	}
}

func TestListHotWithoutStoreReturnsEmpty(t *testing.T) {
	service := NewService(&memoryRepository{items: newItems(1)}, nil, nil)

	result, err := service.List(context.Background(), domain.SceneHot, "", 10)
	if err != nil {
		t.Fatalf("hot list: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected empty hot list, got %d", len(result.Items))
	}
}
