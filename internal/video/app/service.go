package app

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"pulsefeed/internal/shared/cursor"
	"pulsefeed/internal/video/domain"
)

const (
	defaultPageSize = 20
	maxPageSize     = 50
	maxTitleRunes   = 100
	maxURLBytes     = 255
	maxKeyBytes     = 64
)

type CreateInput struct {
	AuthorID       int64
	Title          string
	MediaURL       string
	CoverURL       string
	IdempotencyKey string
}

type ListResult struct {
	Items      []*domain.Video
	NextCursor string
	HasMore    bool
}

type Service struct {
	repo domain.Repository
}

func NewService(repo domain.Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (*domain.Video, error) {
	if input.AuthorID <= 0 {
		return nil, domain.ErrVideoNotFound
	}

	title := strings.TrimSpace(input.Title)
	if count := utf8.RuneCountInString(title); count == 0 || count > maxTitleRunes {
		return nil, domain.ErrInvalidTitle
	}

	mediaURL := strings.TrimSpace(input.MediaURL)
	if !validMediaURL(mediaURL, "video") {
		return nil, domain.ErrInvalidMediaURL
	}

	coverURL := strings.TrimSpace(input.CoverURL)
	if coverURL != "" && !validMediaURL(coverURL, "cover") {
		return nil, domain.ErrInvalidCoverURL
	}

	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if len(idempotencyKey) > maxKeyBytes {
		return nil, domain.ErrInvalidIdempotency
	}

	if idempotencyKey != "" {
		existing, err := s.repo.FindByIdempotencyKey(ctx, input.AuthorID, idempotencyKey)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, domain.ErrVideoNotFound) {
			return nil, err
		}
	}

	video := &domain.Video{
		AuthorID:       input.AuthorID,
		Title:          title,
		MediaURL:       mediaURL,
		CoverURL:       coverURL,
		Status:         domain.StatusPublished,
		IdempotencyKey: idempotencyKey,
		PublishedAt:    time.Now().UTC(),
	}
	stat := &domain.Stat{VideoID: 0}

	if err := s.repo.Create(ctx, video, stat); err != nil {
		if errors.Is(err, domain.ErrIdempotencyConflict) && idempotencyKey != "" {
			return s.repo.FindByIdempotencyKey(ctx, input.AuthorID, idempotencyKey)
		}
		return nil, err
	}
	return video, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*domain.Video, error) {
	if id <= 0 {
		return nil, domain.ErrVideoNotFound
	}
	video, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !video.IsPublished() {
		return nil, domain.ErrVideoNotFound
	}
	return video, nil
}

func (s *Service) ListByAuthor(ctx context.Context, authorID int64, cursorValue string, limit int) (*ListResult, error) {
	if authorID <= 0 {
		return nil, domain.ErrVideoNotFound
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

	videos, err := s.repo.ListByAuthor(ctx, authorID, page, limit+1)
	if err != nil {
		return nil, err
	}

	hasMore := len(videos) > limit
	if hasMore {
		videos = videos[:limit]
	}

	result := &ListResult{Items: videos, HasMore: hasMore}
	if hasMore && len(videos) > 0 {
		last := videos[len(videos)-1]
		result.NextCursor = cursor.Encode(last.PublishedAt, last.ID)
	}
	return result, nil
}

func validMediaURL(value, kind string) bool {
	if value == "" || len(value) > maxURLBytes {
		return false
	}
	if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
		return true
	}
	return strings.HasPrefix(value, "/uploads/"+kind+"/")
}
