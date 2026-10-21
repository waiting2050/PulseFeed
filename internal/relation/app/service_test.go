package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"pulsefeed/internal/relation/domain"
)

type memoryRepository struct {
	users       map[int64]bool
	relations   map[string]bool
	lastLimit   int
	followErr   error
	unfollowErr error
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		users:     map[int64]bool{1: true, 2: true, 3: true},
		relations: map[string]bool{},
	}
}

func relationKey(followerID, followeeID int64) string {
	return fmt.Sprintf("%d:%d", followerID, followeeID)
}

func (r *memoryRepository) UserExists(_ context.Context, userID int64) (bool, error) {
	return r.users[userID], nil
}

func (r *memoryRepository) Follow(_ context.Context, followerID, followeeID int64) (bool, error) {
	if r.followErr != nil {
		return false, r.followErr
	}
	key := relationKey(followerID, followeeID)
	if r.relations[key] {
		return false, nil
	}
	r.relations[key] = true
	return true, nil
}

func (r *memoryRepository) Unfollow(_ context.Context, followerID, followeeID int64) (bool, error) {
	if r.unfollowErr != nil {
		return false, r.unfollowErr
	}
	key := relationKey(followerID, followeeID)
	if !r.relations[key] {
		return false, nil
	}
	delete(r.relations, key)
	return true, nil
}

func (r *memoryRepository) ListFolloweeIDs(_ context.Context, followerID int64, limit int) ([]int64, error) {
	r.lastLimit = limit
	ids := make([]int64, 0, len(r.relations))
	for key, active := range r.relations {
		ownerPart, followeePart, ok := strings.Cut(key, ":")
		if !ok || !active {
			continue
		}
		owner, ownerErr := strconv.ParseInt(ownerPart, 10, 64)
		followee, followeeErr := strconv.ParseInt(followeePart, 10, 64)
		if ownerErr != nil || followeeErr != nil || owner != followerID {
			continue
		}
		ids = append(ids, followee)
		if len(ids) >= limit {
			break
		}
	}
	return ids, nil
}

func TestFollowRejectsSelf(t *testing.T) {
	service := NewService(newMemoryRepository())

	if err := service.Follow(context.Background(), 1, 1); err != domain.ErrCannotFollowSelf {
		t.Fatalf("expected cannot follow self, got %v", err)
	}
}

func TestFollowMissingUser(t *testing.T) {
	service := NewService(newMemoryRepository())

	if err := service.Follow(context.Background(), 1, 999); err != domain.ErrUserNotFound {
		t.Fatalf("expected user not found, got %v", err)
	}
}

func TestFollowAndUnfollowAreIdempotent(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo)
	ctx := context.Background()

	if err := service.Follow(ctx, 1, 2); err != nil {
		t.Fatalf("follow: %v", err)
	}
	if err := service.Follow(ctx, 1, 2); err != nil {
		t.Fatalf("duplicate follow: %v", err)
	}
	if !repo.relations[relationKey(1, 2)] {
		t.Fatal("expected relation stored")
	}

	if err := service.Unfollow(ctx, 1, 2); err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if err := service.Unfollow(ctx, 1, 2); err != nil {
		t.Fatalf("duplicate unfollow: %v", err)
	}
	if repo.relations[relationKey(1, 2)] {
		t.Fatal("expected relation removed")
	}
}

func TestListFolloweeIDsUsesFeedLimit(t *testing.T) {
	repo := newMemoryRepository()
	repo.relations[relationKey(1, 2)] = true
	service := NewService(repo)

	ids, err := service.ListFolloweeIDs(context.Background(), 1)
	if err != nil {
		t.Fatalf("list followees: %v", err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("unexpected followees: %+v", ids)
	}
	if repo.lastLimit != maxFolloweesPerFeed {
		t.Fatalf("expected limit %d, got %d", maxFolloweesPerFeed, repo.lastLimit)
	}
}
