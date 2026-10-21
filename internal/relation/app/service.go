package app

import (
	"context"

	"pulsefeed/internal/relation/domain"
)

// 关注流单次最多取这么多关注对象，避免 IN 查询无上限。
const maxFolloweesPerFeed = 1000

type Service struct {
	repo domain.Repository
}

func NewService(repo domain.Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Follow(ctx context.Context, followerID, followeeID int64) error {
	if followeeID <= 0 {
		return domain.ErrUserNotFound
	}
	if followerID == followeeID {
		return domain.ErrCannotFollowSelf
	}

	exists, err := s.repo.UserExists(ctx, followeeID)
	if err != nil {
		return err
	}
	if !exists {
		return domain.ErrUserNotFound
	}

	_, err = s.repo.Follow(ctx, followerID, followeeID)
	return err
}

func (s *Service) Unfollow(ctx context.Context, followerID, followeeID int64) error {
	if followeeID <= 0 {
		return domain.ErrUserNotFound
	}
	_, err := s.repo.Unfollow(ctx, followerID, followeeID)
	return err
}

func (s *Service) ListFolloweeIDs(ctx context.Context, followerID int64) ([]int64, error) {
	return s.repo.ListFolloweeIDs(ctx, followerID, maxFolloweesPerFeed)
}
