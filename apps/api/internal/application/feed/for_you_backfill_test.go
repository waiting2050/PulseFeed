// for_you_backfill_test.go 覆盖 for-you 推荐的水合补位：已删除条目不再让分页提前截断，
// has_more 以水合结果判定，游标锚定在已返回条目中排序序列的最后一条。
package appfeed_test

import (
	"context"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	apprecommendation "pulsefeed/internal/application/recommendation"
	domfeed "pulsefeed/internal/domain/feed"
	domrecommendation "pulsefeed/internal/domain/recommendation"
)

// filteredCardRepo 只返回 allowed 中的卡片，模拟推荐候选里已删除的作品。
type filteredCardRepo struct {
	*feedFakeRepo
	allowed map[int64]bool
}

func (r *filteredCardRepo) BatchGetEntries(_ context.Context, ids []int64) (map[int64]*domfeed.FeedEntry, error) {
	result := make(map[int64]*domfeed.FeedEntry, len(ids))
	for _, id := range ids {
		if !r.allowed[id] {
			continue
		}
		result[id] = &domfeed.FeedEntry{ClipID: id, CreatorID: 1}
	}
	return result, nil
}

func (r *filteredCardRepo) BatchGetStats(_ context.Context, _ []int64) (map[int64]*domfeed.EntryStats, error) {
	return map[int64]*domfeed.EntryStats{}, nil
}

// recommendCall 记录一次推荐调用，便于核对补位轮数与游标前进位置。
type recommendCall struct {
	cursorScore float64
	cursorClip  int64
	limit       int
}

// fakeRecommender 用内存排序池模拟推荐服务：按 (score, clip_id) 递减过滤游标、截断，
// 再由 display 决定展示顺序（nil 表示保持排序顺序，测试可用它模拟创作者交错）。
type fakeRecommender struct {
	ranked   []domrecommendation.RankedClip
	display  func([]domrecommendation.RankedClip) []domrecommendation.RankedClip
	snapshot time.Time
	calls    []recommendCall
}

func (r *fakeRecommender) Recommend(_ context.Context, _ int64, cursor *domfeed.Cursor, limit int) (*apprecommendation.Result, error) {
	call := recommendCall{limit: limit}
	if cursor != nil {
		call.cursorScore, call.cursorClip = cursor.Score, cursor.ClipID
	}
	r.calls = append(r.calls, call)

	remaining := make([]domrecommendation.RankedClip, 0, len(r.ranked))
	for _, item := range r.ranked {
		if cursor == nil || item.Score < cursor.Score || (item.Score == cursor.Score && item.ClipID < cursor.ClipID) {
			remaining = append(remaining, item)
		}
	}
	result := &apprecommendation.Result{}
	if len(remaining) == 0 {
		return result, nil
	}
	page := remaining
	if len(page) > limit {
		page = page[:limit]
	}
	result.HasMore = len(remaining) > len(page)
	if result.HasMore {
		anchor := page[len(page)-1]
		snapshot := r.snapshot
		result.NextCursor = domfeed.EncodeCursor(&domfeed.Cursor{
			PublishedAt: anchor.PublishedAt,
			ClipID:      anchor.ClipID,
			Score:       anchor.Score,
			SnapshotAt:  &snapshot,
		})
	}
	if r.display != nil {
		result.Items = r.display(page)
	} else {
		result.Items = page
	}
	return result, nil
}

// recommendPool 构造按 (score, clip_id) 递减排列的推荐候选，分值与 clip ID 相同。
func recommendPool(count int) []domrecommendation.RankedClip {
	items := make([]domrecommendation.RankedClip, 0, count)
	for i := count; i >= 1; i-- {
		items = append(items, domrecommendation.RankedClip{
			ClipID:      int64(i),
			CreatorID:   1,
			PublishedAt: time.Date(2026, 1, 2, 10, i, 0, 0, time.UTC),
			Score:       float64(i),
		})
	}
	return items
}

func newForYouService(repo domfeed.Repository, recommender appfeed.Recommender) *appfeed.Service {
	return appfeed.New(repo, appfeed.WithForYouStrategy(recommender))
}

// TestForYouBackfillFillsPageWhenClipsDeleted 验证前几条候选已删除时按推荐顺序补位，
// 补齐一页后给出正确的 has_more 与游标，续页不重复。
func TestForYouBackfillFillsPageWhenClipsDeleted(t *testing.T) {
	ctx := context.Background()
	repo := &filteredCardRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		allowed:      map[int64]bool{3: true, 2: true, 1: true},
	}
	rec := &fakeRecommender{ranked: recommendPool(5), snapshot: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)}
	svc := newForYouService(repo, rec)

	first, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: string(domfeed.SceneForYou), ViewerID: 1, Limit: 2})
	if err != nil {
		t.Fatalf("首页读取失败: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ClipID != 3 || first.Items[1].ClipID != 2 {
		t.Fatalf("补位后应返回 [3 2]，实际 %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("仍有候选时必须给出 has_more 与游标: hasMore=%v cursor=%q", first.HasMore, first.NextCursor)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("首屏应经过两轮推荐读取（首轮被删条挤占后补取一次），实际 %d 轮", len(rec.calls))
	}

	// 游标锚定在已成功返回条目中排序序列的最后一条（score=2），而非被删的 score=4。
	next, err := domfeed.DecodeCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("解析游标失败: %v", err)
	}
	if next.Score != 2 || next.ClipID != 2 || next.SnapshotAt == nil {
		t.Fatalf("游标锚点应为 (2,2) 且携带快照，实际 %+v", next)
	}

	// 续页仍按原游标过滤，不重复返回已给过的条目。
	second, err := svc.GetFeed(ctx, &appfeed.FeedRequest{
		Scene:    string(domfeed.SceneForYou),
		ViewerID: 1,
		Cursor:   first.NextCursor,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("续页读取失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ClipID != 1 {
		t.Fatalf("续页应只返回未读过的 1，实际 %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("候选耗尽应返回短页且无下一页: hasMore=%v cursor=%q", second.HasMore, second.NextCursor)
	}
}

// TestForYouBackfillShortPageWhenCandidatesExhausted 验证候选耗尽时如实返回短页：
// has_more=false 且不生成游标。
func TestForYouBackfillShortPageWhenCandidatesExhausted(t *testing.T) {
	ctx := context.Background()
	repo := &filteredCardRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		allowed:      map[int64]bool{2: true},
	}
	rec := &fakeRecommender{ranked: recommendPool(2), snapshot: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)}
	svc := newForYouService(repo, rec)

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: string(domfeed.SceneForYou), ViewerID: 1, Limit: 5})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ClipID != 2 {
		t.Fatalf("候选耗尽应只返回有效条目 2，实际 %+v", result.Items)
	}
	if result.HasMore || result.NextCursor != "" {
		t.Fatalf("候选耗尽不应声明还有下一页: hasMore=%v cursor=%q", result.HasMore, result.NextCursor)
	}
}

// TestForYouBackfillAnchorUsesRankOrder 验证展示顺序被创作者交错打乱后，
// 游标仍锚定在排序序列的最后一条，避免把展示在前的低分条目重复返回。
func TestForYouBackfillAnchorUsesRankOrder(t *testing.T) {
	ctx := context.Background()
	repo := &filteredCardRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		allowed:      map[int64]bool{1: true, 2: true, 3: true, 4: true, 5: true},
	}
	rec := &fakeRecommender{
		ranked:   recommendPool(5),
		snapshot: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
		// 反转本页顺序，模拟创作者交错后末条分值最高的极端情况。
		display: func(page []domrecommendation.RankedClip) []domrecommendation.RankedClip {
			out := make([]domrecommendation.RankedClip, 0, len(page))
			for i := len(page) - 1; i >= 0; i-- {
				out = append(out, page[i])
			}
			return out
		},
	}
	svc := newForYouService(repo, rec)

	seen := make(map[int64]bool)
	var got []int64
	cursor := ""
	for page := 0; page < 5; page++ {
		result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{
			Scene:    string(domfeed.SceneForYou),
			ViewerID: 1,
			Cursor:   cursor,
			Limit:    2,
		})
		if err != nil {
			t.Fatalf("第 %d 页读取失败: %v", page+1, err)
		}
		for _, item := range result.Items {
			if seen[item.ClipID] {
				t.Fatalf("条目 %d 被重复返回: %v", item.ClipID, got)
			}
			seen[item.ClipID] = true
			got = append(got, item.ClipID)
		}
		if !result.HasMore {
			if result.NextCursor != "" {
				t.Fatalf("末页不应再给游标: %q", result.NextCursor)
			}
			break
		}
		cursor = result.NextCursor
	}

	want := []int64{4, 5, 2, 3, 1}
	if len(got) != len(want) {
		t.Fatalf("翻页结果 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("翻页结果 = %v, 期望 %v", got, want)
		}
	}
}
