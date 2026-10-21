package app

import (
	"context"

	"pulsefeed/internal/relation/domain"
	"pulsefeed/internal/shared/event"
)

// 关注流单次最多取这么多关注对象，避免 IN 查询无上限。
const maxFolloweesPerFeed = 1000

// Publisher 发布关注事件，消息模块订阅后生成通知。
type Publisher interface {
	Publish(ctx context.Context, evt event.Event)
}

type Service struct {
	repo      domain.Repository
	publisher Publisher
}

func NewService(repo domain.Repository, publisher Publisher) *Service {
	return &Service{repo: repo, publisher: publisher}
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

	created, err := s.repo.Follow(ctx, followerID, followeeID)
	if err != nil {
		return err
	}
	if created && s.publisher != nil {
		s.publisher.Publish(ctx, event.Event{
			Type:         event.TypeFollow,
			ActorID:      followerID,
			TargetUserID: followeeID,
		})
	}
	return nil
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
