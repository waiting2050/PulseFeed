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
	repo      domain.Repository
	cache     domain.PageCache
	hot       domain.HotStore
	relations domain.RelationReader
}

func NewService(repo domain.Repository, cache domain.PageCache, hot domain.HotStore, relations domain.RelationReader) *Service {
	return &Service{repo: repo, cache: cache, hot: hot, relations: relations}
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
	case domain.SceneFollowing:
		return nil, domain.ErrLoginRequired
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

	result := paginate(items, limit)
	if s.cache != nil {
		s.cache.Set(ctx, key, result, pageCacheTTL)
	}
	return toResult(result), nil
}

// ListFollowing 只回源当前用户关注的人，关注关系变化频繁，因此不做整页缓存。
func (s *Service) ListFollowing(ctx context.Context, userID int64, cursorValue string, limit int) (*ListResult, error) {
	if userID <= 0 {
		return nil, domain.ErrLoginRequired
	}
	limit = clampLimit(limit)

	var page *cursor.Page
	if cursorValue != "" {
		decoded, err := cursor.Decode(cursorValue)
		if err != nil {
			return nil, domain.ErrInvalidCursor
		}
		page = decoded
	}

	if s.relations == nil {
		return &ListResult{Items: []*domain.Item{}}, nil
	}
	authorIDs, err := s.relations.ListFolloweeIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(authorIDs) == 0 {
		return &ListResult{Items: []*domain.Item{}}, nil
	}

	items, err := s.repo.ListFollowing(ctx, authorIDs, page, limit+1)
	if err != nil {
		return nil, err
	}
	return toResult(paginate(items, limit)), nil
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

func paginate(items []*domain.Item, limit int) *domain.Page {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if items == nil {
		items = []*domain.Item{}
	}

	page := &domain.Page{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		page.NextCursor = cursor.Encode(last.PublishedAt, last.VideoID)
	}
	return page
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
