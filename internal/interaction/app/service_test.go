package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"pulsefeed/internal/interaction/domain"
	"pulsefeed/internal/shared/cursor"
)

type memoryRepository struct {
	videoExists bool
	actions     map[string]bool
	comments    []*domain.Comment
	nextID      int64
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		videoExists: true,
		actions:     map[string]bool{},
		nextID:      1,
	}
}

func actionKey(userID, videoID int64, action int) string {
	return fmt.Sprintf("%d:%d:%d", userID, videoID, action)
}

func (r *memoryRepository) VideoExists(_ context.Context, _ int64) (bool, error) {
	return r.videoExists, nil
}

func (r *memoryRepository) AddAction(_ context.Context, userID, videoID int64, action int) (bool, error) {
	key := actionKey(userID, videoID, action)
	if r.actions[key] {
		return false, nil
	}
	r.actions[key] = true
	return true, nil
}

func (r *memoryRepository) RemoveAction(_ context.Context, userID, videoID int64, action int) (bool, error) {
	key := actionKey(userID, videoID, action)
	if !r.actions[key] {
		return false, nil
	}
	delete(r.actions, key)
	return true, nil
}

func (r *memoryRepository) CreateComment(_ context.Context, comment *domain.Comment) error {
	comment.ID = r.nextID
	r.nextID++
	r.comments = append(r.comments, comment)
	return nil
}

func (r *memoryRepository) FindComment(_ context.Context, id int64) (*domain.Comment, error) {
	for _, comment := range r.comments {
		if comment.ID == id {
			return comment, nil
		}
	}
	return nil, domain.ErrCommentNotFound
}

func (r *memoryRepository) DeleteComment(_ context.Context, id int64) error {
	for i, comment := range r.comments {
		if comment.ID == id {
			r.comments = append(r.comments[:i], r.comments[i+1:]...)
			return nil
		}
	}
	return domain.ErrCommentNotFound
}

func (r *memoryRepository) ListComments(_ context.Context, videoID int64, page *cursor.Page, limit int) ([]*domain.Comment, error) {
	filtered := make([]*domain.Comment, 0, len(r.comments))
	for _, comment := range r.comments {
		if comment.VideoID != videoID {
			continue
		}
		if page != nil && !(comment.CreatedAt.Before(page.PublishedAt) ||
			(comment.CreatedAt.Equal(page.PublishedAt) && comment.ID < page.ID)) {
			continue
		}
		filtered = append(filtered, comment)
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID > filtered[j].ID
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

type scoreCall struct {
	videoID int64
	delta   float64
}

type memoryHotStore struct {
	calls []scoreCall
}

func (h *memoryHotStore) AddScore(_ context.Context, videoID int64, delta float64) error {
	h.calls = append(h.calls, scoreCall{videoID: videoID, delta: delta})
	return nil
}

func TestLikeIsIdempotent(t *testing.T) {
	repo := newMemoryRepository()
	hot := &memoryHotStore{}
	service := NewService(repo, hot)
	ctx := context.Background()

	if err := service.Like(ctx, 1, 100); err != nil {
		t.Fatalf("like: %v", err)
	}
	if err := service.Like(ctx, 1, 100); err != nil {
		t.Fatalf("duplicate like: %v", err)
	}

	if !repo.actions[actionKey(1, 100, domain.ActionLike)] {
		t.Fatal("expected like action stored")
	}
	if len(hot.calls) != 1 || hot.calls[0].delta != domain.HotScoreLike {
		t.Fatalf("unexpected hot score calls: %+v", hot.calls)
	}

	if err := service.Unlike(ctx, 1, 100); err != nil {
		t.Fatalf("unlike: %v", err)
	}
	if err := service.Unlike(ctx, 1, 100); err != nil {
		t.Fatalf("duplicate unlike: %v", err)
	}
	if repo.actions[actionKey(1, 100, domain.ActionLike)] {
		t.Fatal("expected like action removed")
	}
	if len(hot.calls) != 2 || hot.calls[1].delta != -domain.HotScoreLike {
		t.Fatalf("unexpected hot score calls: %+v", hot.calls)
	}
}

func TestFavoriteUsesHigherWeight(t *testing.T) {
	repo := newMemoryRepository()
	hot := &memoryHotStore{}
	service := NewService(repo, hot)

	if err := service.Favorite(context.Background(), 1, 100); err != nil {
		t.Fatalf("favorite: %v", err)
	}
	if len(hot.calls) != 1 || hot.calls[0].delta != domain.HotScoreFavorite {
		t.Fatalf("unexpected hot score calls: %+v", hot.calls)
	}
}

func TestLikeMissingVideo(t *testing.T) {
	repo := newMemoryRepository()
	repo.videoExists = false
	hot := &memoryHotStore{}
	service := NewService(repo, hot)

	if err := service.Like(context.Background(), 1, 100); err != domain.ErrVideoNotFound {
		t.Fatalf("expected video not found, got %v", err)
	}
	if len(hot.calls) != 0 {
		t.Fatalf("expected no hot score calls, got %+v", hot.calls)
	}
}

func TestCreateAndDeleteComment(t *testing.T) {
	repo := newMemoryRepository()
	hot := &memoryHotStore{}
	service := NewService(repo, hot)
	ctx := context.Background()

	comment, err := service.CreateComment(ctx, 8, 100, "  你好  ")
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if comment.Content != "你好" {
		t.Fatalf("expected trimmed content, got %q", comment.Content)
	}
	if len(hot.calls) != 1 || hot.calls[0].delta != domain.HotScoreComment {
		t.Fatalf("unexpected hot score calls: %+v", hot.calls)
	}

	if err := service.DeleteComment(ctx, 9, comment.ID); err != domain.ErrForbidden {
		t.Fatalf("expected forbidden, got %v", err)
	}
	if _, err := repo.FindComment(ctx, comment.ID); err != nil {
		t.Fatalf("comment should still exist: %v", err)
	}

	if err := service.DeleteComment(ctx, 8, comment.ID); err != nil {
		t.Fatalf("delete comment: %v", err)
	}
	if len(hot.calls) != 2 || hot.calls[1].delta != -domain.HotScoreComment {
		t.Fatalf("unexpected hot score calls: %+v", hot.calls)
	}
}

func TestCreateCommentValidatesContent(t *testing.T) {
	service := NewService(newMemoryRepository(), nil)
	ctx := context.Background()

	if _, err := service.CreateComment(ctx, 1, 100, "   "); err != domain.ErrInvalidComment {
		t.Fatalf("expected invalid comment, got %v", err)
	}
	if _, err := service.CreateComment(ctx, 1, 100, strings.Repeat("字", maxCommentRunes+1)); err != domain.ErrInvalidComment {
		t.Fatalf("expected invalid comment, got %v", err)
	}
	if _, err := service.CreateComment(ctx, 1, 100, strings.Repeat("字", maxCommentRunes)); err != nil {
		t.Fatalf("expected max length comment accepted, got %v", err)
	}
}

func TestListCommentsPagination(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo, nil)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if _, err := service.CreateComment(ctx, 1, 100, fmt.Sprintf("comment-%d", i)); err != nil {
			t.Fatalf("create comment: %v", err)
		}
	}

	first, err := service.ListComments(ctx, 100, "", 2)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(first.Items) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(first.Items))
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("expected has_more with cursor, got %+v", first)
	}
	if first.Items[0].Content != "comment-3" || first.Items[1].Content != "comment-2" {
		t.Fatalf("unexpected order: %q, %q", first.Items[0].Content, first.Items[1].Content)
	}

	second, err := service.ListComments(ctx, 100, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("next page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Content != "comment-1" {
		t.Fatalf("unexpected next page: %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("expected last page, got %+v", second)
	}
}

func TestListCommentsRejectsInvalidCursor(t *testing.T) {
	service := NewService(newMemoryRepository(), nil)

	if _, err := service.ListComments(context.Background(), 100, "not-base64", 10); err != domain.ErrInvalidCursor {
		t.Fatalf("expected invalid cursor, got %v", err)
	}
}

func TestCreateCommentTimestampIsUTC(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo, nil)

	comment, err := service.CreateComment(context.Background(), 1, 100, "hello")
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}
	if comment.CreatedAt.Location() != time.UTC {
		t.Fatalf("expected UTC timestamp, got %s", comment.CreatedAt.Location())
	}
}
