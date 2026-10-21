package infra

import (
	"context"

	feeddomain "pulsefeed/internal/feed/domain"
)

// FollowingRecall 把关注作者的最新作品作为第二路召回。
type FollowingRecall struct {
	relations feeddomain.RelationReader
	repo      feeddomain.Repository
}

func NewFollowingRecall(relations feeddomain.RelationReader, repo feeddomain.Repository) *FollowingRecall {
	return &FollowingRecall{relations: relations, repo: repo}
}

func (r *FollowingRecall) Recall(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if r.relations == nil || r.repo == nil || limit <= 0 {
		return nil, nil
	}

	authorIDs, err := r.relations.ListFolloweeIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(authorIDs) == 0 {
		return nil, nil
	}

	items, err := r.repo.ListFollowing(ctx, authorIDs, nil, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.VideoID)
	}
	return ids, nil
}
