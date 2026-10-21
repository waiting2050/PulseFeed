package app

import (
	"context"
	"errors"
	"testing"
	"time"

	feeddomain "pulsefeed/internal/feed/domain"
	"pulsefeed/internal/recommend/domain"
)

type fakeSource struct {
	ids []int64
	err error
}

func (s *fakeSource) Recall(_ context.Context, _ int64, _ int) ([]int64, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.ids, nil
}

type fakeCards struct {
	items map[int64]*feeddomain.Item
	err   error
}

func (r *fakeCards) ListByIDs(_ context.Context, ids []int64) ([]*feeddomain.Item, error) {
	if r.err != nil {
		return nil, r.err
	}
	cards := make([]*feeddomain.Item, 0, len(ids))
	for _, id := range ids {
		if card, ok := r.items[id]; ok {
			cards = append(cards, card)
		}
	}
	return cards, nil
}

type fakeExposed struct {
	seen      map[int64]bool
	filterErr error
	markErr   error
	marked    []int64
}

func (e *fakeExposed) Filter(_ context.Context, _ int64, ids []int64) ([]int64, error) {
	if e.filterErr != nil {
		return ids, e.filterErr
	}
	kept := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !e.seen[id] {
			kept = append(kept, id)
		}
	}
	return kept, nil
}

func (e *fakeExposed) Mark(_ context.Context, _ int64, ids []int64) error {
	if e.markErr != nil {
		return e.markErr
	}
	e.marked = append(e.marked, ids...)
	return nil
}

func newCard(id, authorID int64) *feeddomain.Item {
	return &feeddomain.Item{
		VideoID:     id,
		AuthorID:    authorID,
		Title:       "video",
		PublishedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestListMergesAndDeduplicatesSources(t *testing.T) {
	cards := &fakeCards{items: map[int64]*feeddomain.Item{
		1: newCard(1, 10),
		2: newCard(2, 11),
		3: newCard(3, 12),
	}}
	service := NewService([]domain.RecallSource{
		&fakeSource{ids: []int64{1, 2}},
		&fakeSource{ids: []int64{2, 3}},
	}, cards, &fakeExposed{seen: map[int64]bool{}})

	items, err := service.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	for i, want := range []int64{1, 2, 3} {
		if items[i].VideoID != want {
			t.Fatalf("unexpected order at %d: got %d want %d", i, items[i].VideoID, want)
		}
	}
}

func TestListFiltersExposedAndMarksResults(t *testing.T) {
	cards := &fakeCards{items: map[int64]*feeddomain.Item{
		1: newCard(1, 10),
		2: newCard(2, 11),
		3: newCard(3, 12),
	}}
	exposed := &fakeExposed{seen: map[int64]bool{1: true}}
	service := NewService([]domain.RecallSource{
		&fakeSource{ids: []int64{1, 2, 3}},
	}, cards, exposed)

	items, err := service.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(items) != 2 || items[0].VideoID != 2 || items[1].VideoID != 3 {
		t.Fatalf("unexpected items: %+v", items)
	}
	if len(exposed.marked) != 2 || exposed.marked[0] != 2 || exposed.marked[1] != 3 {
		t.Fatalf("unexpected marked exposures: %+v", exposed.marked)
	}
}

func TestListLimitsItemsPerAuthor(t *testing.T) {
	cards := &fakeCards{items: map[int64]*feeddomain.Item{
		1: newCard(1, 10),
		2: newCard(2, 10),
		3: newCard(3, 10),
		4: newCard(4, 11),
		5: newCard(5, 11),
	}}
	service := NewService([]domain.RecallSource{
		&fakeSource{ids: []int64{1, 2, 3, 4, 5}},
	}, cards, &fakeExposed{seen: map[int64]bool{}})

	items, err := service.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 items after author cap, got %d", len(items))
	}
	for i, want := range []int64{1, 2, 4, 5} {
		if items[i].VideoID != want {
			t.Fatalf("unexpected order at %d: got %d want %d", i, items[i].VideoID, want)
		}
	}
}

func TestListIgnoresFailingRecallSource(t *testing.T) {
	cards := &fakeCards{items: map[int64]*feeddomain.Item{2: newCard(2, 11)}}
	service := NewService([]domain.RecallSource{
		&fakeSource{err: errors.New("hot down")},
		&fakeSource{ids: []int64{2}},
	}, cards, &fakeExposed{seen: map[int64]bool{}})

	items, err := service.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(items) != 1 || items[0].VideoID != 2 {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestListKeepsCandidatesWhenExposureFilterFails(t *testing.T) {
	cards := &fakeCards{items: map[int64]*feeddomain.Item{1: newCard(1, 10)}}
	service := NewService([]domain.RecallSource{
		&fakeSource{ids: []int64{1}},
	}, cards, &fakeExposed{seen: map[int64]bool{}, filterErr: errors.New("redis down")})

	items, err := service.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected degraded filtering to keep candidates, got %d", len(items))
	}
}

func TestListClampsLimit(t *testing.T) {
	ids := make([]int64, 0, 60)
	cardMap := make(map[int64]*feeddomain.Item, 60)
	for i := int64(1); i <= 60; i++ {
		ids = append(ids, i)
		cardMap[i] = newCard(i, i)
	}
	service := NewService([]domain.RecallSource{&fakeSource{ids: ids}}, &fakeCards{items: cardMap}, &fakeExposed{seen: map[int64]bool{}})

	items, err := service.List(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(items) != maxLimit {
		t.Fatalf("expected limit %d, got %d", maxLimit, len(items))
	}
}

func TestListRequiresLogin(t *testing.T) {
	service := NewService(nil, &fakeCards{}, nil)

	if _, err := service.List(context.Background(), 0, 10); err != feeddomain.ErrLoginRequired {
		t.Fatalf("expected login required, got %v", err)
	}
}

func TestListWithoutCandidatesReturnsEmpty(t *testing.T) {
	service := NewService(nil, &fakeCards{}, &fakeExposed{seen: map[int64]bool{}})

	items, err := service.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items, got %d", len(items))
	}
}
