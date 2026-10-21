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

func (r *memoryRepository) ListFollowing(_ context.Context, authorIDs []int64, page *cursor.Page, limit int) ([]*domain.Item, error) {
	if r.err != nil {
		return nil, r.err
	}

	wanted := make(map[int64]bool, len(authorIDs))
	for _, id := range authorIDs {
		wanted[id] = true
	}

	items := make([]*domain.Item, 0, len(r.items))
	for _, item := range r.items {
		if !wanted[item.AuthorID] {
			continue
		}
		if page != nil && !(item.PublishedAt.Before(page.PublishedAt) ||
			(item.PublishedAt.Equal(page.PublishedAt) && item.VideoID < page.ID)) {
			continue
		}
		items = append(items, item)
		if len(items) >= limit {
			break
		}
	}
	return items, nil
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
	service := NewService(repo, nil, nil, nil)

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
	service := NewService(repo, nil, nil, nil)

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
	service := NewService(&memoryRepository{items: newItems(1)}, nil, nil, nil)

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
	service := NewService(repo, cache, nil, nil)

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
	service := NewService(repo, cache, nil, nil)

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
	service := NewService(repo, nil, hot, nil)

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
	service := NewService(&memoryRepository{items: newItems(1)}, nil, nil, nil)

	result, err := service.List(context.Background(), domain.SceneHot, "", 10)
	if err != nil {
		t.Fatalf("hot list: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected empty hot list, got %d", len(result.Items))
	}
}

type memoryRelationReader struct {
	ids []int64
	err error
}

func (r *memoryRelationReader) ListFolloweeIDs(_ context.Context, _ int64) ([]int64, error) {
	return r.ids, r.err
}

func followingItems() []*domain.Item {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return []*domain.Item{
		{VideoID: 101, AuthorID: 2, PublishedAt: base},
		{VideoID: 102, AuthorID: 1, PublishedAt: base.Add(-time.Minute)},
		{VideoID: 103, AuthorID: 3, PublishedAt: base.Add(-2 * time.Minute)},
		{VideoID: 104, AuthorID: 10, PublishedAt: base.Add(-3 * time.Minute)},
	}
}

func TestListFollowingOnlyReturnsFollowedAuthors(t *testing.T) {
	repo := &memoryRepository{items: followingItems()}
	service := NewService(repo, nil, nil, &memoryRelationReader{ids: []int64{2, 3}})

	result, err := service.ListFollowing(context.Background(), 1, "", 10)
	if err != nil {
		t.Fatalf("following list: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
	if result.Items[0].VideoID != 101 || result.Items[1].VideoID != 103 {
		t.Fatalf("unexpected items: %d, %d", result.Items[0].VideoID, result.Items[1].VideoID)
	}
}

func TestListFollowingPagination(t *testing.T) {
	repo := &memoryRepository{items: followingItems()}
	service := NewService(repo, nil, nil, &memoryRelationReader{ids: []int64{2, 3}})

	first, err := service.ListFollowing(context.Background(), 1, "", 1)
	if err != nil {
		t.Fatalf("following list: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].VideoID != 101 {
		t.Fatalf("unexpected first page: %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("expected has_more with cursor, got %+v", first)
	}

	second, err := service.ListFollowing(context.Background(), 1, first.NextCursor, 1)
	if err != nil {
		t.Fatalf("following next page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].VideoID != 103 {
		t.Fatalf("unexpected second page: %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("expected last page, got %+v", second)
	}
}

func TestListFollowingWithoutFolloweesReturnsEmpty(t *testing.T) {
	service := NewService(&memoryRepository{items: followingItems()}, nil, nil, &memoryRelationReader{})

	result, err := service.ListFollowing(context.Background(), 1, "", 10)
	if err != nil {
		t.Fatalf("following list: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected empty list, got %d", len(result.Items))
	}
}

func TestListFollowingRequiresLogin(t *testing.T) {
	service := NewService(&memoryRepository{items: followingItems()}, nil, nil, &memoryRelationReader{ids: []int64{2}})

	if _, err := service.ListFollowing(context.Background(), 0, "", 10); err != domain.ErrLoginRequired {
		t.Fatalf("expected login required, got %v", err)
	}
	if _, err := service.List(context.Background(), domain.SceneFollowing, "", 10); err != domain.ErrLoginRequired {
		t.Fatalf("expected login required, got %v", err)
	}
}

func TestListFollowingRejectsInvalidCursor(t *testing.T) {
	service := NewService(&memoryRepository{items: followingItems()}, nil, nil, &memoryRelationReader{ids: []int64{2}})

	if _, err := service.ListFollowing(context.Background(), 1, "not-base64", 10); err != domain.ErrInvalidCursor {
		t.Fatalf("expected invalid cursor, got %v", err)
	}
}
