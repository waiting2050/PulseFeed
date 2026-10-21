package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"pulsefeed/internal/shared/cursor"
	"pulsefeed/internal/video/domain"
)

type memoryRepository struct {
	mu     sync.Mutex
	videos map[int64]*domain.Video
	stats  map[int64]*domain.Stat
	byKey  map[string]int64
	nextID int64
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		videos: make(map[int64]*domain.Video),
		stats:  make(map[int64]*domain.Stat),
		byKey:  make(map[string]int64),
	}
}

func (r *memoryRepository) Create(_ context.Context, video *domain.Video, stat *domain.Stat) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if video.IdempotencyKey != "" {
		if _, exists := r.byKey[idempotencyKey(video.AuthorID, video.IdempotencyKey)]; exists {
			return domain.ErrIdempotencyConflict
		}
	}

	r.nextID++
	now := time.Now().UTC()
	video.ID = r.nextID
	video.CreatedAt = now
	video.UpdatedAt = now
	r.videos[video.ID] = cloneVideo(video)

	statCopy := *stat
	statCopy.VideoID = video.ID
	r.stats[video.ID] = &statCopy

	if video.IdempotencyKey != "" {
		r.byKey[idempotencyKey(video.AuthorID, video.IdempotencyKey)] = video.ID
	}
	return nil
}

func (r *memoryRepository) FindByID(_ context.Context, id int64) (*domain.Video, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	video, exists := r.videos[id]
	if !exists || !video.IsPublished() {
		return nil, domain.ErrVideoNotFound
	}
	return cloneVideo(video), nil
}

func (r *memoryRepository) FindByIdempotencyKey(_ context.Context, authorID int64, key string) (*domain.Video, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, exists := r.byKey[idempotencyKey(authorID, key)]
	if !exists {
		return nil, domain.ErrVideoNotFound
	}
	video := r.videos[id]
	if !video.IsPublished() {
		return nil, domain.ErrVideoNotFound
	}
	return cloneVideo(video), nil
}

func (r *memoryRepository) ListByAuthor(_ context.Context, authorID int64, page *cursor.Page, limit int) ([]*domain.Video, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matched []*domain.Video
	for _, video := range r.videos {
		if video.AuthorID != authorID || !video.IsPublished() {
			continue
		}
		if page != nil {
			if video.PublishedAt.After(page.PublishedAt) {
				continue
			}
			if video.PublishedAt.Equal(page.PublishedAt) && video.ID >= page.ID {
				continue
			}
		}
		matched = append(matched, cloneVideo(video))
	}

	sort.Slice(matched, func(i, j int) bool {
		if matched[i].PublishedAt.Equal(matched[j].PublishedAt) {
			return matched[i].ID > matched[j].ID
		}
		return matched[i].PublishedAt.After(matched[j].PublishedAt)
	})

	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (r *memoryRepository) setPublishedAt(id int64, publishedAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if video, exists := r.videos[id]; exists {
		video.PublishedAt = publishedAt
	}
}

func idempotencyKey(authorID int64, key string) string {
	return fmt.Sprintf("%d:%s", authorID, key)
}

func cloneVideo(video *domain.Video) *domain.Video {
	cloned := *video
	return &cloned
}

func newTestService() *Service {
	return NewService(newMemoryRepository())
}

func validCreateInput() CreateInput {
	return CreateInput{
		AuthorID: 1,
		Title:    "  第一条视频  ",
		MediaURL: "/uploads/video/abc.mp4",
		CoverURL: "/uploads/cover/abc.jpg",
	}
}

func TestCreateVideo(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		service := newTestService()
		input := validCreateInput()
		input.IdempotencyKey = "create-1"

		video, err := service.Create(context.Background(), input)
		if err != nil {
			t.Fatalf("create video: %v", err)
		}
		if video.Title != "第一条视频" {
			t.Fatalf("title not trimmed: %q", video.Title)
		}
		if video.Status != domain.StatusPublished || !video.IsPublished() {
			t.Fatalf("unexpected status: %d", video.Status)
		}
		if video.PublishedAt.IsZero() || video.ID <= 0 {
			t.Fatalf("unexpected video identity: %+v", video)
		}
	})

	t.Run("idempotent retry", func(t *testing.T) {
		service := newTestService()
		input := validCreateInput()
		input.IdempotencyKey = "same-key"

		first, err := service.Create(context.Background(), input)
		if err != nil {
			t.Fatalf("first create: %v", err)
		}
		second, err := service.Create(context.Background(), input)
		if err != nil {
			t.Fatalf("second create: %v", err)
		}
		if first.ID != second.ID {
			t.Fatalf("expected same video id, got %d and %d", first.ID, second.ID)
		}
	})

	t.Run("different keys create different videos", func(t *testing.T) {
		service := newTestService()
		firstInput := validCreateInput()
		firstInput.IdempotencyKey = "key-a"
		secondInput := validCreateInput()
		secondInput.IdempotencyKey = "key-b"

		first, err := service.Create(context.Background(), firstInput)
		if err != nil {
			t.Fatalf("first create: %v", err)
		}
		second, err := service.Create(context.Background(), secondInput)
		if err != nil {
			t.Fatalf("second create: %v", err)
		}
		if first.ID == second.ID {
			t.Fatal("different idempotency keys must create different videos")
		}
	})

	t.Run("invalid title", func(t *testing.T) {
		service := newTestService()
		input := validCreateInput()
		input.Title = "   "
		if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidTitle) {
			t.Fatalf("expected ErrInvalidTitle, got %v", err)
		}

		input.Title = strings.Repeat("长", 101)
		if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidTitle) {
			t.Fatalf("expected ErrInvalidTitle, got %v", err)
		}
	})

	t.Run("invalid media url", func(t *testing.T) {
		service := newTestService()
		input := validCreateInput()
		input.MediaURL = "ftp://example.com/video.mp4"
		if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidMediaURL) {
			t.Fatalf("expected ErrInvalidMediaURL, got %v", err)
		}
	})

	t.Run("invalid cover url", func(t *testing.T) {
		service := newTestService()
		input := validCreateInput()
		input.CoverURL = "/etc/passwd"
		if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidCoverURL) {
			t.Fatalf("expected ErrInvalidCoverURL, got %v", err)
		}
	})

	t.Run("idempotency key too long", func(t *testing.T) {
		service := newTestService()
		input := validCreateInput()
		input.IdempotencyKey = strings.Repeat("k", 65)
		if _, err := service.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalidIdempotency) {
			t.Fatalf("expected ErrInvalidIdempotency, got %v", err)
		}
	})
}

func TestGetVideo(t *testing.T) {
	service := newTestService()
	video, err := service.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("create video: %v", err)
	}

	found, err := service.Get(context.Background(), video.ID)
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if found.ID != video.ID {
		t.Fatalf("unexpected video id: %d", found.ID)
	}

	if _, err := service.Get(context.Background(), 9999); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Fatalf("expected ErrVideoNotFound, got %v", err)
	}
	if _, err := service.Get(context.Background(), 0); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Fatalf("expected ErrVideoNotFound for zero id, got %v", err)
	}
}

func TestListByAuthor(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)

	var ids []int64
	for i := 0; i < 3; i++ {
		input := validCreateInput()
		input.Title = fmt.Sprintf("video-%d", i)
		video, err := service.Create(context.Background(), input)
		if err != nil {
			t.Fatalf("create video %d: %v", i, err)
		}
		ids = append(ids, video.ID)
	}

	base := time.Now().UTC().Add(-time.Hour)
	repository.setPublishedAt(ids[0], base)
	repository.setPublishedAt(ids[1], base.Add(time.Second))
	repository.setPublishedAt(ids[2], base.Add(2*time.Second))

	firstPage, err := service.ListByAuthor(context.Background(), 1, "", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(firstPage.Items) != 2 || !firstPage.HasMore {
		t.Fatalf("unexpected first page: %d items, has_more=%v", len(firstPage.Items), firstPage.HasMore)
	}
	if firstPage.Items[0].ID != ids[2] || firstPage.Items[1].ID != ids[1] {
		t.Fatalf("unexpected order: %d, %d", firstPage.Items[0].ID, firstPage.Items[1].ID)
	}
	if firstPage.NextCursor == "" {
		t.Fatal("expected next cursor")
	}

	secondPage, err := service.ListByAuthor(context.Background(), 1, firstPage.NextCursor, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.HasMore {
		t.Fatalf("unexpected second page: %d items, has_more=%v", len(secondPage.Items), secondPage.HasMore)
	}
	if secondPage.Items[0].ID != ids[0] {
		t.Fatalf("unexpected last item: %d", secondPage.Items[0].ID)
	}

	if _, err := service.ListByAuthor(context.Background(), 1, "not-a-cursor", 2); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("expected ErrInvalidCursor, got %v", err)
	}
}

func TestListByAuthorDefaultsLimit(t *testing.T) {
	service := newTestService()
	for i := 0; i < 25; i++ {
		input := validCreateInput()
		input.Title = fmt.Sprintf("video-%d", i)
		if _, err := service.Create(context.Background(), input); err != nil {
			t.Fatalf("create video %d: %v", i, err)
		}
	}

	result, err := service.ListByAuthor(context.Background(), 1, "", 0)
	if err != nil {
		t.Fatalf("list videos: %v", err)
	}
	if len(result.Items) != defaultPageSize || !result.HasMore {
		t.Fatalf("unexpected default page: %d items, has_more=%v", len(result.Items), result.HasMore)
	}
}
