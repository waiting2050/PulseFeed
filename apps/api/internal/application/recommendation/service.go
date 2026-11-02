// service.go 编排推荐用例：固定快照候选召回、兴趣向量聚合、加权排序、游标过滤与交错后生成下一页游标。
package apprecommendation

import (
	"context"
	"errors"
	"time"

	domembedding "pulsefeed/internal/domain/embedding"
	domfeed "pulsefeed/internal/domain/feed"
	domrecommendation "pulsefeed/internal/domain/recommendation"
)

// 内部曝光决策原因常量，供 /internal/impression-decisions 返回可读依据。
const (
	// DecisionNoImpression 表示读者没有该 clip 的曝光记录，可正常展示。
	DecisionNoImpression = "no_recent_impression"
	// DecisionRecentlyImpressed 表示最近 7 天内已曝光过，推荐阶段排除。
	DecisionRecentlyImpressed = "recently_impressed"
	// DecisionOutsideWindow 表示曝光早于 7 天窗口，可再次展示。
	DecisionOutsideWindow = "outside_impression_window"
)

// EmbeddingReader 是向量批量读取端口，由 infra/persistence/embedding 实现。
type EmbeddingReader interface {
	BatchGet(ctx context.Context, clipIDs []int64) (map[int64]domembedding.Vector, error)
}

// Result 是一页推荐结果；Items 已按创作者交错用于展示，NextCursor 携带 snapshot_at 与排序键。
type Result struct {
	Items      []domrecommendation.RankedClip
	NextCursor string
	HasMore    bool
}

// CandidateView 是内部接口展示的候选召回详情。
type CandidateView struct {
	ClipID       int64
	CreatorID    int64
	PublishedAt  time.Time
	HotScore     float64
	HasEmbedding bool
}

// Service 提供推荐读取用例，只依赖领域端口。
type Service struct {
	repo       domrecommendation.Repository
	embeddings EmbeddingReader
}

// New 创建推荐应用服务。
func New(repo domrecommendation.Repository, embeddings EmbeddingReader) *Service {
	return &Service{repo: repo, embeddings: embeddings}
}

// Recommend 读取一页 for-you 推荐：
// 首屏以当前时刻为快照，后续页沿用游标中的 snapshot_at；
// 候选召回 → 加权排序 → 按 (score, clip_id) 严格小于游标过滤 → 截断 → 交错展示，
// 下一页游标取本页在排序序列中的最后一条（分数最小者），保证跨分钟翻页不重不漏。
func (s *Service) Recommend(ctx context.Context, viewerID int64, cursor *domfeed.Cursor, limit int) (*Result, error) {
	if viewerID <= 0 {
		return nil, domrecommendation.ErrViewerRequired
	}
	if limit <= 0 {
		return nil, domrecommendation.ErrInvalidLimit
	}

	snapshotAt := time.Now()
	if cursor != nil {
		if cursor.SnapshotAt == nil {
			return nil, domrecommendation.ErrInvalidCursor
		}
		snapshotAt = *cursor.SnapshotAt
	}

	interest, err := s.interestVector(ctx, viewerID, snapshotAt)
	if err != nil {
		return nil, err
	}

	candidates, err := s.repo.ListCandidates(ctx, viewerID, snapshotAt, candidatePoolSize(limit))
	if err != nil {
		return nil, err
	}
	ranked := domrecommendation.Rank(candidates, interest, snapshotAt)
	if cursor != nil {
		ranked = filterBelow(ranked, cursor.Score, cursor.ClipID)
	}

	result := &Result{}
	if len(ranked) == 0 {
		return result, nil
	}

	result.HasMore = len(ranked) > limit
	page := ranked
	if result.HasMore {
		page = ranked[:limit]
	}
	if len(page) == 0 {
		return result, nil
	}

	result.Items = domrecommendation.InterleaveByCreator(page)
	if result.HasMore {
		// 游标锚点取排序序列末尾（本页分数最小者）：下一页严格小于该键，
		// 既不会重复本页任一条目，也不会跳过本页之外的候选。
		anchor := page[len(page)-1]
		snapshot := snapshotAt
		result.NextCursor = domfeed.EncodeCursor(&domfeed.Cursor{
			PublishedAt: anchor.PublishedAt,
			ClipID:      anchor.ClipID,
			Score:       anchor.Score,
			SnapshotAt:  &snapshot,
		})
	}
	return result, nil
}

// ListCandidates 返回内部候选召回详情，按当前时刻快照计算热度分；limit 上限为 CandidatePoolMax。
func (s *Service) ListCandidates(ctx context.Context, viewerID int64, limit int) ([]CandidateView, error) {
	if viewerID <= 0 {
		return nil, domrecommendation.ErrViewerRequired
	}
	if limit <= 0 || limit > domrecommendation.CandidatePoolMax {
		return nil, domrecommendation.ErrInvalidLimit
	}

	now := time.Now()
	candidates, err := s.repo.ListCandidates(ctx, viewerID, now, limit)
	if err != nil {
		return nil, err
	}
	views := make([]CandidateView, 0, len(candidates))
	for _, candidate := range candidates {
		views = append(views, CandidateView{
			ClipID:       candidate.ClipID,
			CreatorID:    candidate.CreatorID,
			PublishedAt:  candidate.PublishedAt,
			HotScore:     domrecommendation.ClipHotScore(candidate, now),
			HasEmbedding: len(candidate.Embedding) > 0,
		})
	}
	return views, nil
}

// ImpressionDecision 判断某 clip 是否可再次进入推荐候选：7 天内曝光过则排除。
func (s *Service) ImpressionDecision(ctx context.Context, viewerID, clipID int64) (*domrecommendation.ImpressionDecision, error) {
	if viewerID <= 0 {
		return nil, domrecommendation.ErrViewerRequired
	}
	if clipID <= 0 {
		return nil, domrecommendation.ErrInvalidLimit
	}

	record, err := s.repo.GetImpression(ctx, viewerID, clipID)
	if err != nil {
		if errors.Is(err, domrecommendation.ErrImpressionNotFound) {
			return &domrecommendation.ImpressionDecision{ShouldShow: true, Reason: DecisionNoImpression}, nil
		}
		return nil, err
	}

	windowSince := time.Now().Add(-time.Duration(domrecommendation.ImpressionWindowDays) * 24 * time.Hour)
	if record.LastPlayedAt.After(windowSince) {
		lastPlayedAt := record.LastPlayedAt
		return &domrecommendation.ImpressionDecision{
			ShouldShow:   false,
			Reason:       DecisionRecentlyImpressed,
			LastPlayedAt: &lastPlayedAt,
		}, nil
	}
	lastPlayedAt := record.LastPlayedAt
	return &domrecommendation.ImpressionDecision{
		ShouldShow:   true,
		Reason:       DecisionOutsideWindow,
		LastPlayedAt: &lastPlayedAt,
	}, nil
}

// ListImpressions 返回读者最近 days 天内的曝光聚合记录；days 上限 365、limit 上限 500。
func (s *Service) ListImpressions(ctx context.Context, viewerID int64, days, limit int) ([]*domrecommendation.ImpressionRecord, error) {
	if viewerID <= 0 {
		return nil, domrecommendation.ErrViewerRequired
	}
	if days <= 0 || days > 365 {
		return nil, domrecommendation.ErrInvalidLimit
	}
	if limit <= 0 || limit > domrecommendation.CandidatePoolMax {
		return nil, domrecommendation.ErrInvalidLimit
	}

	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return s.repo.ListImpressions(ctx, viewerID, since, limit)
}

// interestVector 聚合读者近 30 天曝光 clip 的向量：按 play + 2*complete 加权求和后 L2 归一化；
// 无行为或向量缺失时返回 nil，排序时兴趣分按 0 处理。
func (s *Service) interestVector(ctx context.Context, viewerID int64, snapshotAt time.Time) (domembedding.Vector, error) {
	since := snapshotAt.Add(-time.Duration(domrecommendation.InterestWindowDays) * 24 * time.Hour)
	clips, err := s.repo.ListInterestClips(ctx, viewerID, since, snapshotAt, domrecommendation.InterestVectorLimit)
	if err != nil {
		return nil, err
	}
	if len(clips) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(clips))
	for _, clip := range clips {
		ids = append(ids, clip.ClipID)
	}
	vectors, err := s.embeddings.BatchGet(ctx, ids)
	if err != nil {
		return nil, err
	}

	sum := make(domembedding.Vector, domembedding.Dimension)
	totalWeight := 0.0
	for _, clip := range clips {
		vector, ok := vectors[clip.ClipID]
		if !ok {
			continue
		}
		weight := domrecommendation.InterestWeight(clip.PlayCount, clip.CompletedCount)
		if weight <= 0 {
			continue
		}
		for i := range sum {
			sum[i] += weight * vector[i]
		}
		totalWeight += weight
	}
	if totalWeight == 0 {
		return nil, nil
	}
	return sum.Normalized(), nil
}

// filterBelow 保留 (score, clip_id) 严格小于游标锚点的候选，保证翻页不重复。
func filterBelow(items []domrecommendation.RankedClip, score float64, clipID int64) []domrecommendation.RankedClip {
	kept := items[:0]
	for _, item := range items {
		if item.Score < score || (item.Score == score && item.ClipID < clipID) {
			kept = append(kept, item)
		}
	}
	return kept
}

// candidatePoolSize 按页大小放大召回量，并夹在 C5 约定的 50..500 区间内。
func candidatePoolSize(limit int) int {
	size := limit * 20
	if size < domrecommendation.CandidatePoolMin {
		size = domrecommendation.CandidatePoolMin
	}
	if size > domrecommendation.CandidatePoolMax {
		size = domrecommendation.CandidatePoolMax
	}
	return size
}
