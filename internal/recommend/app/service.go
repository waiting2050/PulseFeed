package app

import (
	"context"

	feeddomain "pulsefeed/internal/feed/domain"
	"pulsefeed/internal/recommend/domain"
)

const (
	defaultLimit      = 10
	maxLimit          = 30
	recallLimit       = 50
	maxItemsPerAuthor = 2
)

type Service struct {
	sources []domain.RecallSource
	cards   domain.CardRepository
	exposed domain.ExposedStore
}

func NewService(sources []domain.RecallSource, cards domain.CardRepository, exposed domain.ExposedStore) *Service {
	return &Service{sources: sources, cards: cards, exposed: exposed}
}

// List 依次完成多路召回、跨路去重、曝光过滤、作者打散，最后记录本页曝光。
func (s *Service) List(ctx context.Context, userID int64, limit int) ([]*feeddomain.Item, error) {
	if userID <= 0 {
		return nil, feeddomain.ErrLoginRequired
	}
	limit = clampLimit(limit)

	ids := s.recall(ctx, userID)
	if len(ids) == 0 {
		return []*feeddomain.Item{}, nil
	}

	if s.exposed != nil {
		filtered, err := s.exposed.Filter(ctx, userID, ids)
		if err == nil {
			ids = filtered
		}
	}
	if len(ids) == 0 {
		return []*feeddomain.Item{}, nil
	}

	cards, err := s.cards.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]*feeddomain.Item, len(cards))
	for _, card := range cards {
		byID[card.VideoID] = card
	}

	authorCount := make(map[int64]int, limit)
	items := make([]*feeddomain.Item, 0, limit)
	exposedIDs := make([]int64, 0, limit)
	for _, id := range ids {
		card, ok := byID[id]
		if !ok {
			continue
		}
		if authorCount[card.AuthorID] >= maxItemsPerAuthor {
			continue
		}
		authorCount[card.AuthorID]++
		items = append(items, card)
		exposedIDs = append(exposedIDs, id)
		if len(items) >= limit {
			break
		}
	}

	if s.exposed != nil && len(exposedIDs) > 0 {
		_ = s.exposed.Mark(ctx, userID, exposedIDs)
	}
	return items, nil
}

// 召回失败只丢弃该路结果，避免单路故障拖垮整个推荐页。
func (s *Service) recall(ctx context.Context, userID int64) []int64 {
	merged := make([]int64, 0, len(s.sources)*recallLimit)
	seen := make(map[int64]struct{}, len(merged))
	for _, source := range s.sources {
		ids, err := source.Recall(ctx, userID, recallLimit)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
	}
	return merged
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
