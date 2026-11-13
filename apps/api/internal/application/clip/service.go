// service.go 编排 clip 幂等发布、详情读取、创作者列表与删除用例，只依赖领域端口。
package appclip

import (
	"context"
	"errors"
	"log"

	domclip "pulsefeed/internal/domain/clip"
)

// ListResult 是创作者 clip 列表的分页结果。
type ListResult struct {
	Items      []*domclip.Clip
	NextCursor string
	HasMore    bool
}

// Option 用于装配可选依赖。
type Option func(*Service)

// FeedCacheInvalidator 描述删除作品后需要失效的 Feed 缓存能力，由 infra/cache 实现。
// 应用层只依赖该端口，不直接感知 Redis。
type FeedCacheInvalidator interface {
	// DeleteClipCache 删除单个 clip 的卡片与计数缓存。
	DeleteClipCache(ctx context.Context, clipID int64)
	// BumpFeedVersion 递增页缓存版本，使既有 Feed 页缓存整体失效。
	BumpFeedVersion(ctx context.Context)
}

// WithPublisher 注入 clip.published 事件发布器；未注入时发布成功但不投递事件。
func WithPublisher(p Publisher) Option {
	return func(s *Service) {
		s.publisher = p
	}
}

// WithFeedCacheInvalidator 注入删除后的 Feed 缓存失效器；未注入时跳过失效（仅靠 TTL）。
func WithFeedCacheInvalidator(inv FeedCacheInvalidator) Option {
	return func(s *Service) {
		s.invalidator = inv
	}
}

// Service 提供 clip 相关用例。
type Service struct {
	repo        domclip.Repository
	publisher   Publisher
	invalidator FeedCacheInvalidator
}

// New 创建 clip 应用服务。
func New(repo domclip.Repository, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreatePublished 幂等发布 clip：先按 (creatorID, 幂等键) 查重，命中直接返回既有记录；
// 未命中走事务写入，唯一索引冲突（并发重试）时回读既有记录；成功后投递 clip.published。
// durationMS 来自上传探测，缺失时为 0，不参与发布校验；description 可选，空串合法。
func (s *Service) CreatePublished(ctx context.Context, creatorID int64, title, description, videoURL, coverURL string, durationMS int64, idempotencyKey string) (*domclip.Clip, error) {
	existing, err := s.repo.GetByIdempotencyKey(ctx, creatorID, idempotencyKey)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, domclip.ErrClipNotFound) {
		return nil, err
	}

	clip, err := domclip.NewClip(creatorID, title, description, videoURL, coverURL, durationMS, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, clip); err != nil {
		if errors.Is(err, domclip.ErrClipExists) {
			// 并发下同键请求已被其他事务写入，回读保证返回同一 clip_id。
			return s.repo.GetByIdempotencyKey(ctx, creatorID, idempotencyKey)
		}
		return nil, err
	}

	if s.publisher != nil {
		event := ClipPublished{ClipID: clip.ID, CreatorID: clip.CreatorID, PublishedAt: clip.PublishedAt}
		if err := s.publisher.PublishClipPublished(ctx, event); err != nil {
			// 事件投递失败不回滚发布结果，MQ 链路在 S10 接入后由 Worker 保证最终一致。
			log.Printf("投递 clip.published 事件失败 (clip_id=%d): %v", clip.ID, err)
		}
		if err := s.publisher.PublishClipEmbedding(ctx, event); err != nil {
			// 向量事件失败不阻塞发布；S14 的推荐链路可容忍向量暂时缺失。
			log.Printf("投递 clip.embedding 事件失败 (clip_id=%d): %v", clip.ID, err)
		}
	}
	return clip, nil
}

// Get 读取 clip 详情；不存在或已删除统一返回 ErrClipNotFound。
func (s *Service) Get(ctx context.Context, clipID int64) (*domclip.Clip, error) {
	clip, err := s.repo.GetByID(ctx, clipID)
	if err != nil {
		return nil, err
	}
	if !clip.Visible() {
		return nil, domclip.ErrClipNotFound
	}
	return clip, nil
}

// ListByCreator 按游标分页读取创作者可见 clip；limit 越界返回 ErrInvalidLimit，非正取默认值。
func (s *Service) ListByCreator(ctx context.Context, creatorID int64, cursorStr string, limit int) (*ListResult, error) {
	if limit < 0 || limit > domclip.MaxListLimit {
		return nil, domclip.ErrInvalidLimit
	}
	if limit == 0 {
		limit = domclip.DefaultListLimit
	}
	cursor, err := domclip.DecodeCursor(cursorStr)
	if err != nil {
		return nil, err
	}

	// 多取一条用于判定是否还有下一页，返回前截断。
	items, err := s.repo.ListByCreator(ctx, creatorID, cursor, limit+1)
	if err != nil {
		return nil, err
	}

	result := &ListResult{Items: items}
	if len(items) > limit {
		result.HasMore = true
		result.Items = items[:limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = domclip.EncodeCursor(&domclip.Cursor{PublishedAt: last.PublishedAt, ClipID: last.ID})
	}
	return result, nil
}

// Delete 删除 clip：仅创作者本人可操作，重复删除幂等；
// 数据库删除成功后失效卡片/计数缓存并递增页缓存版本，避免已删除内容继续出现在 Feed。
func (s *Service) Delete(ctx context.Context, requesterID, clipID int64, idempotencyKey string) error {
	clip, err := s.repo.GetByID(ctx, clipID)
	if err != nil {
		return err
	}
	if clip.CreatorID != requesterID {
		return domclip.ErrNotClipOwner
	}
	if err := s.repo.Delete(ctx, clipID, idempotencyKey); err != nil {
		return err
	}
	if s.invalidator != nil {
		s.invalidator.DeleteClipCache(ctx, clipID)
		s.invalidator.BumpFeedVersion(ctx)
	}
	return nil
}
