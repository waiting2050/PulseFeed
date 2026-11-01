// service.go 编排关注关系用例：关注/取关状态机、关注/粉丝列表游标分页；
// 关注状态真正迁移时同步触发 Feed 回填与站内消息（端口可选，失败仅记日志不阻塞）。
package apprelation

import (
	"context"
	"log"
	"strings"

	domrelation "pulsefeed/internal/domain/relation"
)

// FollowFeedBackfiller 描述关注成功后的 Feed 回填能力，由 feed 应用层实现、组合根注入。
type FollowFeedBackfiller interface {
	// BackfillFollowing 把创作者最近发布回填到读者 inbox（或大 V outbox）。
	BackfillFollowing(ctx context.Context, followerID, creatorID int64) error
}

// FollowEventMessenger 描述关注事件站内消息能力；S16 消息中心落地前注入 nil 跳过。
type FollowEventMessenger interface {
	// NotifyFollowed 向被关注者发送关注通知。
	NotifyFollowed(ctx context.Context, followerID, targetUserID int64) error
}

// Option 用于装配可选依赖。
type Option func(*Service)

// WithFollowFeedBackfiller 注入关注回填器：关注状态迁移后同步回填读者 Feed。
func WithFollowFeedBackfiller(b FollowFeedBackfiller) Option {
	return func(s *Service) { s.backfiller = b }
}

// WithFollowEventMessenger 注入关注站内消息发送器。
func WithFollowEventMessenger(m FollowEventMessenger) Option {
	return func(s *Service) { s.messenger = m }
}

// Service 提供关注关系用例。
type Service struct {
	repo       domrelation.Repository
	backfiller FollowFeedBackfiller
	messenger  FollowEventMessenger
}

// New 创建 relation 应用服务。
func New(repo domrelation.Repository, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Follow 关注目标用户：重复关注幂等返回当前关系；状态真正迁移时同步回填 Feed 并发送站内消息。
func (s *Service) Follow(ctx context.Context, userID, targetUserID int64, idempotencyKey string) (*domrelation.FollowResult, error) {
	return s.setFollow(ctx, userID, targetUserID, true, idempotencyKey)
}

// Unfollow 取消关注目标用户：重复取关幂等返回当前关系；历史 inbox 条目由读路径按实时关注集过滤。
func (s *Service) Unfollow(ctx context.Context, userID, targetUserID int64, idempotencyKey string) (*domrelation.FollowResult, error) {
	return s.setFollow(ctx, userID, targetUserID, false, idempotencyKey)
}

// setFollow 是关注/取关的公共逻辑：基础校验复用领域构造器，关注迁移触发回填与消息，
// 回填/消息失败仅记日志不阻塞主链路。
func (s *Service) setFollow(ctx context.Context, userID, targetUserID int64, follow bool, idempotencyKey string) (*domrelation.FollowResult, error) {
	if _, err := domrelation.NewFollow(userID, targetUserID); err != nil {
		return nil, err
	}
	result, err := s.repo.SetFollow(ctx, userID, targetUserID, follow, strings.TrimSpace(idempotencyKey))
	if err != nil {
		return nil, err
	}
	if follow && result.Changed {
		if s.backfiller != nil {
			if err := s.backfiller.BackfillFollowing(ctx, userID, targetUserID); err != nil {
				log.Printf("warning: 关注回填失败 (user_id=%d target_user_id=%d): %v", userID, targetUserID, err)
			}
		}
		if s.messenger != nil {
			if err := s.messenger.NotifyFollowed(ctx, userID, targetUserID); err != nil {
				log.Printf("warning: 关注站内消息发送失败 (user_id=%d target_user_id=%d): %v", userID, targetUserID, err)
			}
		}
	}
	return result, nil
}

// ListResult 是关注/粉丝列表的分页结果。
type ListResult struct {
	Items      []*domrelation.ListEntry
	NextCursor string
	HasMore    bool
}

// ListFollowing 分页返回用户的关注列表，按 (updated_at, target_user_id) 严格递减。
func (s *Service) ListFollowing(ctx context.Context, userID int64, cursor string, limit int) (*ListResult, error) {
	parsed, size, err := parseListPage(cursor, limit)
	if err != nil {
		return nil, err
	}
	entries, err := s.repo.ListFollowing(ctx, userID, parsed, size+1)
	if err != nil {
		return nil, err
	}
	return buildListResult(entries, size), nil
}

// ListFollowers 分页返回用户的粉丝列表，按 (updated_at, user_id) 严格递减，分页语义同上。
func (s *Service) ListFollowers(ctx context.Context, userID int64, cursor string, limit int) (*ListResult, error) {
	parsed, size, err := parseListPage(cursor, limit)
	if err != nil {
		return nil, err
	}
	entries, err := s.repo.ListFollowers(ctx, userID, parsed, size+1)
	if err != nil {
		return nil, err
	}
	return buildListResult(entries, size), nil
}

// parseListPage 解析列表游标并校验页大小：0 取默认值，负值或超上限返回 ErrInvalidLimit。
func parseListPage(cursor string, limit int) (*domrelation.ListCursor, int, error) {
	if limit < 0 || limit > domrelation.MaxLimit {
		return nil, 0, domrelation.ErrInvalidLimit
	}
	size := limit
	if size == 0 {
		size = domrelation.DefaultLimit
	}
	parsed, err := domrelation.DecodeListCursor(cursor)
	if err != nil {
		return nil, 0, err
	}
	return parsed, size, nil
}

// buildListResult 截断多取的一条用于判定 has_more，并以页尾条目生成下一页游标。
func buildListResult(entries []*domrelation.ListEntry, size int) *ListResult {
	result := &ListResult{Items: entries}
	if len(entries) > size {
		result.HasMore = true
		result.Items = entries[:size]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = domrelation.EncodeListCursor(&domrelation.ListCursor{FollowedAt: last.FollowedAt, UserID: last.UserID})
	}
	return result
}
