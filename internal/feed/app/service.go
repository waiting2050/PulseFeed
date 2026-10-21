package app

import (
	"context"
	"fmt"
	"time"

	"pulsefeed/internal/feed/domain"
	"pulsefeed/internal/shared/cursor"
)

const (
	defaultPageSize = 10
	maxPageSize     = 30
	pageCacheTTL    = 60 * time.Second
	cacheKeyPrefix  = "feed:page:v1"

	hotWindow    = 60 * time.Minute
	hotScanLimit = 200
)

type ListResult struct {
	Items      []*domain.Item
	NextCursor string
	HasMore    bool
}

type Service struct {
	repo  domain.Repository
	cache domain.PageCache
	hot   domain.HotStore
}

func NewService(repo domain.Repository, cache domain.PageCache, hot domain.HotStore) *Service {
	return &Service{repo: repo, cache: cache, hot: hot}
}

func (s *Service) List(ctx context.Context, scene, cursorValue string, limit int) (*ListResult, error) {
	if scene == "" {
		scene = domain.SceneTimeline
	}
	limit = clampLimit(limit)

	switch scene {
	case domain.SceneTimeline:
		return s.listTimeline(ctx, cursorValue, limit)
	case domain.SceneHot:
		return s.listHot(ctx, cursorValue, limit)
	default:
		return nil, domain.ErrInvalidScene
	}
}

func (s *Service) listTimeline(ctx context.Context, cursorValue string, limit int) (*ListResult, error) {
	var page *cursor.Page
	if cursorValue != "" {
		decoded, err := cursor.Decode(cursorValue)
		if err != nil {
			return nil, domain.ErrInvalidCursor
		}
		page = decoded
	}

	key := cacheKey(domain.SceneTimeline, limit, cursorValue)
	if s.cache != nil {
		if cached, ok := s.cache.Get(ctx, key); ok {
			return toResult(cached), nil
		}
	}

	items, err := s.repo.ListTimeline(ctx, page, limit+1)
	if err != nil {
		return nil, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if items == nil {
		items = []*domain.Item{}
	}

	result := &domain.Page{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		result.NextCursor = cursor.Encode(last.PublishedAt, last.VideoID)
	}
	if s.cache != nil {
		s.cache.Set(ctx, key, result, pageCacheTTL)
	}
	return toResult(result), nil
}

func (s *Service) listHot(ctx context.Context, cursorValue string, limit int) (*ListResult, error) {
	var rank *cursor.Rank
	if cursorValue != "" {
		decoded, err := cursor.DecodeRank(cursorValue)
		if err != nil {
			return nil, domain.ErrInvalidCursor
		}
		rank = decoded
	}

	key := cacheKey(domain.SceneHot, limit, cursorValue)
	if s.cache != nil {
		if cached, ok := s.cache.Get(ctx, key); ok {
			return toResult(cached), nil
		}
	}

	if s.hot == nil {
		return &ListResult{Items: []*domain.Item{}}, nil
	}
	entries, err := s.hot.TopScores(ctx, hotWindow, hotScanLimit)
	if err != nil {
		return nil, err
	}
	if rank != nil {
		entries = entriesAfter(entries, rank)
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}

	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.VideoID)
	}
	cards, err := s.repo.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := &domain.Page{Items: orderByIDs(cards, ids), HasMore: hasMore}
	if hasMore && len(entries) > 0 {
		last := entries[len(entries)-1]
		result.NextCursor = cursor.EncodeRank(last.Score, last.VideoID)
	}
	if s.cache != nil {
		s.cache.Set(ctx, key, result, pageCacheTTL)
	}
	return toResult(result), nil
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

func entriesAfter(entries []domain.ScoreEntry, rank *cursor.Rank) []domain.ScoreEntry {
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.Score < rank.Score || (entry.Score == rank.Score && entry.VideoID < rank.ID) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func orderByIDs(cards []*domain.Item, ids []int64) []*domain.Item {
	byID := make(map[int64]*domain.Item, len(cards))
	for _, card := range cards {
		byID[card.VideoID] = card
	}

	ordered := make([]*domain.Item, 0, len(cards))
	for _, id := range ids {
		if card, ok := byID[id]; ok {
			ordered = append(ordered, card)
		}
	}
	return ordered
}

func cacheKey(scene string, limit int, cursorValue string) string {
	if cursorValue == "" {
		cursorValue = "head"
	}
	return fmt.Sprintf("%s:%s:%d:%s", cacheKeyPrefix, scene, limit, cursorValue)
}

func toResult(page *domain.Page) *ListResult {
	items := page.Items
	if items == nil {
		items = []*domain.Item{}
	}
	return &ListResult{Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore}
}
