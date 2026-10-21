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
)

type ListResult struct {
	Items      []*domain.Item
	NextCursor string
	HasMore    bool
}

type Service struct {
	repo  domain.Repository
	cache domain.PageCache
}

func NewService(repo domain.Repository, cache domain.PageCache) *Service {
	return &Service{repo: repo, cache: cache}
}

func (s *Service) List(ctx context.Context, scene, cursorValue string, limit int) (*ListResult, error) {
	if scene == "" {
		scene = domain.SceneTimeline
	}
	if scene != domain.SceneTimeline {
		return nil, domain.ErrInvalidScene
	}
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}

	var page *cursor.Page
	if cursorValue != "" {
		decoded, err := cursor.Decode(cursorValue)
		if err != nil {
			return nil, domain.ErrInvalidCursor
		}
		page = decoded
	}

	key := cacheKey(scene, limit, cursorValue)
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
	if s.cache != nil {
		s.cache.Set(ctx, key, result, pageCacheTTL)
	}
	return toResult(result), nil
}

func cacheKey(scene string, limit int, cursorValue string) string {
	if cursorValue == "" {
		cursorValue = "head"
	}
	return fmt.Sprintf("%s:%s:%d:%s", cacheKeyPrefix, scene, limit, cursorValue)
}

func toResult(page *domain.Page) *ListResult {
	result := &ListResult{Items: page.Items, HasMore: page.HasMore}
	if len(page.Items) == 0 {
		result.Items = []*domain.Item{}
		return result
	}
	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		result.NextCursor = cursor.Encode(last.PublishedAt, last.VideoID)
	}
	return result
}
