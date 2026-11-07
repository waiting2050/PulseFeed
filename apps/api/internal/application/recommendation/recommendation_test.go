// recommendation_test.go 用内存 fake 验证 for-you 推荐的跨页游标不重不漏、快照时间稳定、
// 热度排序、兴趣向量参与打分以及最近曝光窗口的展示决策。
package apprecommendation_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	apprecommendation "pulsefeed/internal/application/recommendation"
	domembedding "pulsefeed/internal/domain/embedding"
	domfeed "pulsefeed/internal/domain/feed"
	domrecommendation "pulsefeed/internal/domain/recommendation"
)

// recommendationFakeRepository 是推荐仓储的内存替身：记录快照参数并按快照过滤候选。
type recommendationFakeRepository struct {
	candidates      []domrecommendation.Candidate
	interestClips   []domrecommendation.InterestClip
	impressions     map[int64]*domrecommendation.ImpressionRecord
	impressionList  []*domrecommendation.ImpressionRecord
	candidateErr    error
	interestErr     error
	listErr         error
	impressionErr   error
	candidateCalls  int
	impressionCalls int
	snapshots       []time.Time
	limits          []int
	interestSince   []time.Time
	interestUntil   []time.Time
}

var _ domrecommendation.Repository = (*recommendationFakeRepository)(nil)

// newRecommendationFakeRepository 创建内存推荐仓储。
func newRecommendationFakeRepository() *recommendationFakeRepository {
	return &recommendationFakeRepository{
		impressions: make(map[int64]*domrecommendation.ImpressionRecord),
	}
}

// ListCandidates 记录快照与召回上限，并模拟发布态/最近曝光窗口的候选过滤。
func (f *recommendationFakeRepository) ListCandidates(_ context.Context, _ int64, snapshotAt time.Time, limit int) ([]domrecommendation.Candidate, error) {
	f.candidateCalls++
	f.snapshots = append(f.snapshots, snapshotAt)
	f.limits = append(f.limits, limit)
	if f.candidateErr != nil {
		return nil, f.candidateErr
	}

	windowSince := snapshotAt.Add(-time.Duration(domrecommendation.ImpressionWindowDays) * 24 * time.Hour)
	picked := make([]domrecommendation.Candidate, 0, len(f.candidates))
	for _, candidate := range f.candidates {
		if candidate.PublishedAt.After(snapshotAt) {
			continue
		}
		if record, ok := f.impressions[candidate.ClipID]; ok {
			if !record.LastPlayedAt.Before(windowSince) && !record.LastPlayedAt.After(snapshotAt) {
				continue
			}
		}
		picked = append(picked, candidate)
	}
	if len(picked) > limit {
		picked = picked[:limit]
	}
	return picked, nil
}

// ListInterestClips 记录兴趣窗口并返回预置的曝光行为。
func (f *recommendationFakeRepository) ListInterestClips(_ context.Context, _ int64, since, until time.Time, _ int) ([]domrecommendation.InterestClip, error) {
	f.interestSince = append(f.interestSince, since)
	f.interestUntil = append(f.interestUntil, until)
	if f.interestErr != nil {
		return nil, f.interestErr
	}
	return f.interestClips, nil
}

// GetImpression 返回预置的曝光记录，未预置时返回领域未找到错误。
func (f *recommendationFakeRepository) GetImpression(_ context.Context, _ int64, clipID int64) (*domrecommendation.ImpressionRecord, error) {
	f.impressionCalls++
	if f.impressionErr != nil {
		return nil, f.impressionErr
	}
	if record, ok := f.impressions[clipID]; ok {
		return record, nil
	}
	return nil, domrecommendation.ErrImpressionNotFound
}

// ListImpressions 返回预置的曝光记录列表。
func (f *recommendationFakeRepository) ListImpressions(_ context.Context, _ int64, _ time.Time, limit int) ([]*domrecommendation.ImpressionRecord, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.impressionList) > limit {
		return f.impressionList[:limit], nil
	}
	return f.impressionList, nil
}

// recommendationFakeEmbeddingReader 是向量批量读取端口的内存替身，记录每次请求的 clip 集合。
type recommendationFakeEmbeddingReader struct {
	vectors  map[int64]domembedding.Vector
	requests [][]int64
	err      error
}

var _ apprecommendation.EmbeddingReader = (*recommendationFakeEmbeddingReader)(nil)

// BatchGet 返回预置向量，缺失的 clip 缺席于结果集。
func (f *recommendationFakeEmbeddingReader) BatchGet(_ context.Context, clipIDs []int64) (map[int64]domembedding.Vector, error) {
	f.requests = append(f.requests, append([]int64(nil), clipIDs...))
	if f.err != nil {
		return nil, f.err
	}
	found := make(map[int64]domembedding.Vector, len(clipIDs))
	for _, clipID := range clipIDs {
		if vector, ok := f.vectors[clipID]; ok {
			found[clipID] = vector
		}
	}
	return found, nil
}

// recommendationFakeAxisVector 生成仅第 index 维为 1 的 256 维单位向量。
func recommendationFakeAxisVector(index int) domembedding.Vector {
	vector := make(domembedding.Vector, domembedding.Dimension)
	vector[index] = 1
	return vector
}

// recommendationFakeCandidates 生成 7 条发布时间递增、热度递增的候选。
func recommendationFakeCandidates() []domrecommendation.Candidate {
	publishedAt := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	candidates := make([]domrecommendation.Candidate, 0, 7)
	for i := int64(1); i <= 7; i++ {
		candidates = append(candidates, domrecommendation.Candidate{
			ClipID:      i,
			CreatorID:   i * 10,
			PublishedAt: publishedAt.Add(time.Duration(i) * time.Minute),
			LikeCount:   i,
		})
	}
	return candidates
}

// TestRecommendPaginatesWithStableSnapshot 验证跨页翻页不重不漏，且快照时间在各页保持不变。
func TestRecommendPaginatesWithStableSnapshot(t *testing.T) {
	repo := newRecommendationFakeRepository()
	repo.candidates = recommendationFakeCandidates()
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})

	var cursorText string
	var ordered []int64
	seen := make(map[int64]int, len(repo.candidates))
	var firstSnapshot time.Time
	pages := 0
	for {
		pages++
		if pages > 10 {
			t.Fatalf("翻页未终止，已读取 %d 页", pages)
		}

		var cursor *domfeed.Cursor
		if cursorText != "" {
			decoded, err := domfeed.DecodeCursor(cursorText)
			if err != nil {
				t.Fatalf("第 %d 页游标解码失败: %v", pages, err)
			}
			cursor = decoded
		}

		result, err := service.Recommend(context.Background(), 7, cursor, 2)
		if err != nil {
			t.Fatalf("第 %d 页推荐失败: %v", pages, err)
		}
		if len(result.Items) == 0 {
			t.Fatalf("第 %d 页为空", pages)
		}
		for _, item := range result.Items {
			seen[item.ClipID]++
			ordered = append(ordered, item.ClipID)
		}

		if !result.HasMore {
			if result.NextCursor != "" {
				t.Fatalf("末页不应返回游标, 实际 %q", result.NextCursor)
			}
			break
		}
		if result.NextCursor == "" {
			t.Fatalf("有更多数据时游标不能为空")
		}

		decoded, err := domfeed.DecodeCursor(result.NextCursor)
		if err != nil {
			t.Fatalf("第 %d 页游标解码失败: %v", pages, err)
		}
		if decoded.SnapshotAt == nil {
			t.Fatalf("推荐游标必须携带 snapshot_at")
		}
		if firstSnapshot.IsZero() {
			firstSnapshot = *decoded.SnapshotAt
		} else if !decoded.SnapshotAt.Equal(firstSnapshot) {
			t.Fatalf("跨页快照时间发生漂移: %v != %v", decoded.SnapshotAt, firstSnapshot)
		}
		cursorText = result.NextCursor
	}

	if len(seen) != len(repo.candidates) {
		t.Fatalf("翻页覆盖 clip 数 = %d, 期望 %d（seen=%v）", len(seen), len(repo.candidates), seen)
	}
	for clipID, count := range seen {
		if count != 1 {
			t.Fatalf("clip %d 被返回 %d 次, 期望 1", clipID, count)
		}
	}
	if len(ordered) != len(repo.candidates) {
		t.Fatalf("累计条目数 = %d, 期望 %d", len(ordered), len(repo.candidates))
	}
	if len(repo.snapshots) != pages {
		t.Fatalf("候选召回次数 = %d, 期望 %d", len(repo.snapshots), pages)
	}

	// 各页必须使用首屏快照，且展示顺序与同一快照下的排序序列一致（候选创作者各不相同）。
	want := domrecommendation.Rank(repo.candidates, nil, repo.snapshots[0])
	for i, item := range want {
		if ordered[i] != item.ClipID {
			t.Fatalf("第 %d 条 = %d, 期望 %d（实际顺序=%v）", i, ordered[i], item.ClipID, ordered)
		}
	}
	for _, snapshot := range repo.snapshots[1:] {
		if !snapshot.Equal(repo.snapshots[0]) {
			t.Fatalf("后续页快照 = %v, 期望与首屏一致 %v", snapshot, repo.snapshots[0])
		}
	}
}

// TestRecommendRejectsInvalidArguments 验证推荐入参与游标缺少 snapshot_at 时的错误。
func TestRecommendRejectsInvalidArguments(t *testing.T) {
	repo := newRecommendationFakeRepository()
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})
	ctx := context.Background()

	if _, err := service.Recommend(ctx, 0, nil, 2); !errors.Is(err, domrecommendation.ErrViewerRequired) {
		t.Fatalf("viewer_id=0 错误 = %v, 期望 %v", err, domrecommendation.ErrViewerRequired)
	}
	if _, err := service.Recommend(ctx, 7, nil, 0); !errors.Is(err, domrecommendation.ErrInvalidLimit) {
		t.Fatalf("limit=0 错误 = %v, 期望 %v", err, domrecommendation.ErrInvalidLimit)
	}
	if _, err := service.Recommend(ctx, 7, &domfeed.Cursor{ClipID: 3, Score: 1}, 2); !errors.Is(err, domrecommendation.ErrInvalidCursor) {
		t.Fatalf("缺少 snapshot_at 错误 = %v, 期望 %v", err, domrecommendation.ErrInvalidCursor)
	}
}

// TestRecommendStopsWhenCandidatesUnderLimit 验证候选不足一页时不返回游标。
func TestRecommendStopsWhenCandidatesUnderLimit(t *testing.T) {
	repo := newRecommendationFakeRepository()
	publishedAt := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	repo.candidates = []domrecommendation.Candidate{
		{ClipID: 1, CreatorID: 11, PublishedAt: publishedAt, LikeCount: 1},
		{ClipID: 2, CreatorID: 12, PublishedAt: publishedAt, LikeCount: 2},
	}
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})

	result, err := service.Recommend(context.Background(), 7, nil, 5)
	if err != nil {
		t.Fatalf("推荐失败: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("条目数 = %d, 期望 2", len(result.Items))
	}
	if result.HasMore || result.NextCursor != "" {
		t.Fatalf("候选不足一页时不应标记更多或返回游标, has_more=%v cursor=%q", result.HasMore, result.NextCursor)
	}
}

// TestRecommendReturnsEmptyResultWithoutCandidates 验证空候选返回空结果而非错误。
func TestRecommendReturnsEmptyResultWithoutCandidates(t *testing.T) {
	repo := newRecommendationFakeRepository()
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})

	result, err := service.Recommend(context.Background(), 7, nil, 2)
	if err != nil {
		t.Fatalf("空候选推荐失败: %v", err)
	}
	if len(result.Items) != 0 || result.HasMore || result.NextCursor != "" {
		t.Fatalf("空候选结果 = %+v, 期望全空", result)
	}
}

// TestListCandidatesComputesHotScoreWithSnapshot 验证内部候选召回按快照基准计算热度分并标注向量是否存在；
// 候选顺序由仓储契约（热度降序）保证，本用例只断言服务层保留仓储顺序与分数口径。
func TestListCandidatesComputesHotScoreWithSnapshot(t *testing.T) {
	repo := newRecommendationFakeRepository()
	publishedAt := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	repo.candidates = []domrecommendation.Candidate{
		{ClipID: 1, CreatorID: 11, PublishedAt: publishedAt, LikeCount: 1},
		{ClipID: 2, CreatorID: 12, PublishedAt: publishedAt, LikeCount: 5, Embedding: recommendationFakeAxisVector(0)},
		{ClipID: 3, CreatorID: 13, PublishedAt: publishedAt, LikeCount: 3},
	}
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})

	views, err := service.ListCandidates(context.Background(), 7, domrecommendation.CandidatePoolMin)
	if err != nil {
		t.Fatalf("候选召回失败: %v", err)
	}
	if len(repo.snapshots) != 1 {
		t.Fatalf("候选召回次数 = %d, 期望 1", len(repo.snapshots))
	}
	if repo.limits[0] != domrecommendation.CandidatePoolMin {
		t.Fatalf("召回上限 = %d, 期望 %d", repo.limits[0], domrecommendation.CandidatePoolMin)
	}

	byClipID := make(map[int64]domrecommendation.Candidate, len(repo.candidates))
	for _, candidate := range repo.candidates {
		byClipID[candidate.ClipID] = candidate
	}

	// 仓储实现按热度降序召回；fake 返回预置顺序，服务层应原样保留。
	wantOrder := []int64{1, 2, 3}
	if len(views) != len(wantOrder) {
		t.Fatalf("候选视图数 = %d, 期望 %d", len(views), len(wantOrder))
	}
	for i, view := range views {
		if view.ClipID != wantOrder[i] {
			t.Fatalf("第 %d 条 clip_id = %d, 期望 %d", i, view.ClipID, wantOrder[i])
		}
		candidate := byClipID[view.ClipID]
		wantHot := domrecommendation.ClipHotScore(candidate, repo.snapshots[0])
		if view.HotScore != wantHot {
			t.Fatalf("clip %d 热度分 = %.12f, 期望 %.12f", view.ClipID, view.HotScore, wantHot)
		}
		if view.HasEmbedding != (len(candidate.Embedding) > 0) {
			t.Fatalf("clip %d HasEmbedding = %v, 期望 %v", view.ClipID, view.HasEmbedding, len(candidate.Embedding) > 0)
		}
		if !view.PublishedAt.Equal(candidate.PublishedAt) {
			t.Fatalf("clip %d 发布时间 = %v, 期望 %v", view.ClipID, view.PublishedAt, candidate.PublishedAt)
		}
	}
}

// TestListCandidatesRejectsInvalidArguments 验证候选召回的入参校验。
func TestListCandidatesRejectsInvalidArguments(t *testing.T) {
	repo := newRecommendationFakeRepository()
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})
	ctx := context.Background()

	if _, err := service.ListCandidates(ctx, 0, domrecommendation.CandidatePoolMin); !errors.Is(err, domrecommendation.ErrViewerRequired) {
		t.Fatalf("viewer_id=0 错误 = %v, 期望 %v", err, domrecommendation.ErrViewerRequired)
	}
	if _, err := service.ListCandidates(ctx, 7, 0); !errors.Is(err, domrecommendation.ErrInvalidLimit) {
		t.Fatalf("limit=0 错误 = %v, 期望 %v", err, domrecommendation.ErrInvalidLimit)
	}
	if _, err := service.ListCandidates(ctx, 7, domrecommendation.CandidatePoolMax+1); !errors.Is(err, domrecommendation.ErrInvalidLimit) {
		t.Fatalf("limit 超上限错误 = %v, 期望 %v", err, domrecommendation.ErrInvalidLimit)
	}
}

// TestRecommendBuildsInterestVectorFromImpressions 验证近 30 天曝光行为经向量加权参与排序。
func TestRecommendBuildsInterestVectorFromImpressions(t *testing.T) {
	repo := newRecommendationFakeRepository()
	publishedAt := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	repo.candidates = []domrecommendation.Candidate{
		// clip_id 更小，兴趣对齐时应在同分情况下排前。
		{ClipID: 1, CreatorID: 11, PublishedAt: publishedAt, LikeCount: 1, Embedding: recommendationFakeAxisVector(0)},
		// clip_id 更大但兴趣正交，无兴趣时应按兜底排前。
		{ClipID: 2, CreatorID: 12, PublishedAt: publishedAt, LikeCount: 1, Embedding: recommendationFakeAxisVector(1)},
	}
	repo.interestClips = []domrecommendation.InterestClip{{ClipID: 50, PlayCount: 3}}
	embeddings := &recommendationFakeEmbeddingReader{
		vectors: map[int64]domembedding.Vector{50: recommendationFakeAxisVector(0)},
	}
	service := apprecommendation.New(repo, embeddings)

	result, err := service.Recommend(context.Background(), 7, nil, 2)
	if err != nil {
		t.Fatalf("推荐失败: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("条目数 = %d, 期望 2", len(result.Items))
	}
	if result.Items[0].ClipID != 1 {
		t.Fatalf("兴趣对齐候选未排首: %v", result.Items)
	}

	scoreGap := result.Items[0].Score - result.Items[1].Score
	if math.Abs(scoreGap-domrecommendation.WeightInterest) > 1e-9 {
		t.Fatalf("兴趣加权分差 = %.12f, 期望 %.2f", scoreGap, domrecommendation.WeightInterest)
	}

	if len(repo.interestSince) != 1 || len(repo.interestUntil) != 1 {
		t.Fatalf("兴趣查询次数 = %d/%d, 期望各 1", len(repo.interestSince), len(repo.interestUntil))
	}
	snapshot := repo.snapshots[0]
	wantSince := snapshot.Add(-time.Duration(domrecommendation.InterestWindowDays) * 24 * time.Hour)
	if !repo.interestSince[0].Equal(wantSince) {
		t.Fatalf("兴趣窗口起点 = %v, 期望 %v", repo.interestSince[0], wantSince)
	}
	if !repo.interestUntil[0].Equal(snapshot) {
		t.Fatalf("兴趣窗口终点 = %v, 期望快照时间 %v", repo.interestUntil[0], snapshot)
	}
	if len(embeddings.requests) != 1 || len(embeddings.requests[0]) != 1 || embeddings.requests[0][0] != 50 {
		t.Fatalf("向量批量读取请求 = %v, 期望 [[50]]", embeddings.requests)
	}
}

// TestImpressionDecisionByWindow 验证最近 7 天曝光排除、窗口外可再次展示与无记录可展示。
func TestImpressionDecisionByWindow(t *testing.T) {
	repo := newRecommendationFakeRepository()
	recent := time.Now().Add(-24 * time.Hour)
	repo.impressions[10] = &domrecommendation.ImpressionRecord{ViewerID: 7, ClipID: 10, LastPlayedAt: recent}
	old := time.Now().Add(-8 * 24 * time.Hour)
	repo.impressions[11] = &domrecommendation.ImpressionRecord{ViewerID: 7, ClipID: 11, LastPlayedAt: old}
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})
	ctx := context.Background()

	recentDecision, err := service.ImpressionDecision(ctx, 7, 10)
	if err != nil {
		t.Fatalf("查询最近曝光决策失败: %v", err)
	}
	if recentDecision.ShouldShow {
		t.Fatalf("最近 7 天内曝光过的 clip 不应再次展示")
	}
	if recentDecision.Reason != apprecommendation.DecisionRecentlyImpressed {
		t.Fatalf("决策原因 = %q, 期望 %q", recentDecision.Reason, apprecommendation.DecisionRecentlyImpressed)
	}
	if recentDecision.LastPlayedAt == nil || !recentDecision.LastPlayedAt.Equal(recent) {
		t.Fatalf("最近播放时间 = %v, 期望 %v", recentDecision.LastPlayedAt, recent)
	}

	oldDecision, err := service.ImpressionDecision(ctx, 7, 11)
	if err != nil {
		t.Fatalf("查询窗口外决策失败: %v", err)
	}
	if !oldDecision.ShouldShow {
		t.Fatalf("窗口外曝光不应继续排除")
	}
	if oldDecision.Reason != apprecommendation.DecisionOutsideWindow {
		t.Fatalf("决策原因 = %q, 期望 %q", oldDecision.Reason, apprecommendation.DecisionOutsideWindow)
	}

	missingDecision, err := service.ImpressionDecision(ctx, 7, 12)
	if err != nil {
		t.Fatalf("无曝光记录应返回可展示决策, 实际错误 %v", err)
	}
	if !missingDecision.ShouldShow {
		t.Fatalf("无曝光记录应可展示")
	}
	if missingDecision.Reason != apprecommendation.DecisionNoImpression {
		t.Fatalf("决策原因 = %q, 期望 %q", missingDecision.Reason, apprecommendation.DecisionNoImpression)
	}
	if missingDecision.LastPlayedAt != nil {
		t.Fatalf("无曝光记录不应返回播放时间, 实际 %v", missingDecision.LastPlayedAt)
	}
}

// TestImpressionDecisionRejectsInvalidArguments 验证曝光决策的入参校验。
func TestImpressionDecisionRejectsInvalidArguments(t *testing.T) {
	repo := newRecommendationFakeRepository()
	service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})
	ctx := context.Background()

	if _, err := service.ImpressionDecision(ctx, 0, 10); !errors.Is(err, domrecommendation.ErrViewerRequired) {
		t.Fatalf("viewer_id=0 错误 = %v, 期望 %v", err, domrecommendation.ErrViewerRequired)
	}
	if _, err := service.ImpressionDecision(ctx, 7, 0); !errors.Is(err, domrecommendation.ErrInvalidLimit) {
		t.Fatalf("clip_id=0 错误 = %v, 期望 %v", err, domrecommendation.ErrInvalidLimit)
	}
}

// TestRecommendPropagatesRepositoryError 验证候选召回、兴趣行为与向量读取失败均向上返回。
func TestRecommendPropagatesRepositoryError(t *testing.T) {
	errSentinel := errors.New("仓储不可用")

	t.Run("候选召回失败", func(t *testing.T) {
		repo := newRecommendationFakeRepository()
		repo.candidateErr = errSentinel
		service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})
		if _, err := service.Recommend(context.Background(), 7, nil, 2); err == nil {
			t.Fatalf("候选召回失败应返回错误")
		}
	})

	t.Run("兴趣行为查询失败", func(t *testing.T) {
		repo := newRecommendationFakeRepository()
		repo.interestErr = errSentinel
		service := apprecommendation.New(repo, &recommendationFakeEmbeddingReader{})
		if _, err := service.Recommend(context.Background(), 7, nil, 2); err == nil {
			t.Fatalf("兴趣行为查询失败应返回错误")
		}
	})

	t.Run("向量读取失败", func(t *testing.T) {
		repo := newRecommendationFakeRepository()
		repo.interestClips = []domrecommendation.InterestClip{{ClipID: 50, PlayCount: 1}}
		embeddings := &recommendationFakeEmbeddingReader{err: errSentinel}
		service := apprecommendation.New(repo, embeddings)
		if _, err := service.Recommend(context.Background(), 7, nil, 2); err == nil {
			t.Fatalf("向量读取失败应返回错误")
		}
	})
}
