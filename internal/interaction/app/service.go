package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"pulsefeed/internal/interaction/domain"
	"pulsefeed/internal/shared/cursor"
	"pulsefeed/internal/shared/event"
)

const (
	defaultPageSize = 20
	maxPageSize     = 50
	maxCommentRunes = 500
)

type CommentListResult struct {
	Items      []*domain.Comment
	NextCursor string
	HasMore    bool
}

// Publisher 发布互动事件，消息模块订阅后生成通知。
type Publisher interface {
	Publish(ctx context.Context, evt event.Event)
}

type Service struct {
	repo      domain.Repository
	hot       domain.HotStore
	publisher Publisher
}

func NewService(repo domain.Repository, hot domain.HotStore, publisher Publisher) *Service {
	return &Service{repo: repo, hot: hot, publisher: publisher}
}

func (s *Service) Like(ctx context.Context, userID, videoID int64) error {
	return s.applyAction(ctx, userID, videoID, domain.ActionLike, true)
}

func (s *Service) Unlike(ctx context.Context, userID, videoID int64) error {
	return s.applyAction(ctx, userID, videoID, domain.ActionLike, false)
}

func (s *Service) Favorite(ctx context.Context, userID, videoID int64) error {
	return s.applyAction(ctx, userID, videoID, domain.ActionFavorite, true)
}

func (s *Service) Unfavorite(ctx context.Context, userID, videoID int64) error {
	return s.applyAction(ctx, userID, videoID, domain.ActionFavorite, false)
}

// applyAction 复用同一套幂等逻辑：只有行为真正发生变化时才更新热榜分数，
// 重复点赞或取消不存在的点赞不会重复计分。
func (s *Service) applyAction(ctx context.Context, userID, videoID int64, action int, active bool) error {
	if videoID <= 0 {
		return domain.ErrVideoNotFound
	}
	if err := s.ensureVideo(ctx, videoID); err != nil {
		return err
	}
	authorID, err := s.repo.VideoAuthor(ctx, videoID)
	if err != nil {
		return err
	}

	var changed bool
	if active {
		changed, err = s.repo.AddAction(ctx, userID, videoID, action)
	} else {
		changed, err = s.repo.RemoveAction(ctx, userID, videoID, action)
	}
	if err != nil {
		return err
	}

	if changed {
		delta := hotScore(action)
		if !active {
			delta = -delta
		}
		s.addScore(ctx, videoID, delta)
		if active {
			s.publish(ctx, event.Event{
				Type:         eventType(action),
				ActorID:      userID,
				TargetUserID: authorID,
				VideoID:      videoID,
			})
		}
	}
	return nil
}

func (s *Service) CreateComment(ctx context.Context, userID, videoID int64, content string) (*domain.Comment, error) {
	if videoID <= 0 {
		return nil, domain.ErrVideoNotFound
	}

	content = strings.TrimSpace(content)
	if count := utf8.RuneCountInString(content); count == 0 || count > maxCommentRunes {
		return nil, domain.ErrInvalidComment
	}

	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}
	authorID, err := s.repo.VideoAuthor(ctx, videoID)
	if err != nil {
		return nil, err
	}

	comment := &domain.Comment{
		VideoID:   videoID,
		UserID:    userID,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.repo.CreateComment(ctx, comment); err != nil {
		return nil, err
	}
	s.addScore(ctx, videoID, domain.HotScoreComment)
	s.publish(ctx, event.Event{
		Type:         event.TypeComment,
		ActorID:      userID,
		TargetUserID: authorID,
		VideoID:      videoID,
		CommentID:    comment.ID,
		Content:      content,
	})
	return comment, nil
}

func (s *Service) DeleteComment(ctx context.Context, userID, commentID int64) error {
	comment, err := s.repo.FindComment(ctx, commentID)
	if err != nil {
		return err
	}
	if comment.UserID != userID {
		return domain.ErrForbidden
	}
	if err := s.repo.DeleteComment(ctx, commentID); err != nil {
		return err
	}
	s.addScore(ctx, comment.VideoID, -domain.HotScoreComment)
	return nil
}

func (s *Service) ListComments(ctx context.Context, videoID int64, cursorValue string, limit int) (*CommentListResult, error) {
	if videoID <= 0 {
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

	if err := s.ensureVideo(ctx, videoID); err != nil {
		return nil, err
	}

	comments, err := s.repo.ListComments(ctx, videoID, page, limit+1)
	if err != nil {
		return nil, err
	}

	hasMore := len(comments) > limit
	if hasMore {
		comments = comments[:limit]
	}
	if comments == nil {
		comments = []*domain.Comment{}
	}

	result := &CommentListResult{Items: comments, HasMore: hasMore}
	if hasMore && len(comments) > 0 {
		last := comments[len(comments)-1]
		result.NextCursor = cursor.Encode(last.CreatedAt, last.ID)
	}
	return result, nil
}

func (s *Service) ensureVideo(ctx context.Context, videoID int64) error {
	exists, err := s.repo.VideoExists(ctx, videoID)
	if err != nil {
		return err
	}
	if !exists {
		return domain.ErrVideoNotFound
	}
	return nil
}

// 热榜是旁路能力，写失败不应影响互动主流程。
func (s *Service) addScore(ctx context.Context, videoID int64, delta float64) {
	if s.hot == nil || delta == 0 {
		return
	}
	_ = s.hot.AddScore(ctx, videoID, delta)
}

func hotScore(action int) float64 {
	switch action {
	case domain.ActionLike:
		return domain.HotScoreLike
	case domain.ActionFavorite:
		return domain.HotScoreFavorite
	default:
		return 0
	}
}

func eventType(action int) event.Type {
	switch action {
	case domain.ActionLike:
		return event.TypeLike
	case domain.ActionFavorite:
		return event.TypeFavorite
	default:
		return ""
	}
}

func (s *Service) publish(ctx context.Context, evt event.Event) {
	if s.publisher == nil {
		return
	}
	s.publisher.Publish(ctx, evt)
}
