// hot_backfill_test.go 覆盖热度窗口水合补取：窗口前段条目已被删除时仍返回满页并保持
// HasMore 正确，候选耗尽时返回短页，多轮补取仍不跳条、不重复。
package appfeed_test

import (
	"context"
	"testing"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// hotBackfillRepo 在内存仓储基础上只返回 allowed 中的卡片，模拟已删除作品残留在热度窗口；
// 同时记录每次批量请求的 ID，用于核对补取的 DB 往返次数。
type hotBackfillRepo struct {
	*feedFakeRepo
	allowed   map[int64]bool
	entryReqs [][]int64
	statsReqs [][]int64
}

func (r *hotBackfillRepo) BatchGetEntries(_ context.Context, ids []int64) (map[int64]*domfeed.FeedEntry, error) {
	r.entryReqs = append(r.entryReqs, append([]int64(nil), ids...))
	result := make(map[int64]*domfeed.FeedEntry, len(ids))
	for _, id := range ids {
		if !r.allowed[id] {
			continue
		}
		result[id] = &domfeed.FeedEntry{ClipID: id, CreatorID: 1}
	}
	return result, nil
}

func (r *hotBackfillRepo) BatchGetStats(_ context.Context, ids []int64) (map[int64]*domfeed.EntryStats, error) {
	r.statsReqs = append(r.statsReqs, append([]int64(nil), ids...))
	return map[int64]*domfeed.EntryStats{}, nil
}

// hotWindow 构造 count 个按 (score, clip_id) 递减排列的热度条目（分值等于 clip ID）。
func hotWindow(count int) []domfeed.HotItem {
	items := make([]domfeed.HotItem, 0, count)
	for i := count; i >= 1; i-- {
		items = append(items, domfeed.HotItem{ClipID: int64(i), Score: float64(i)})
	}
	return items
}

// hotAllowed 把 clip ID 列表转成可水合集合。
func hotAllowed(ids ...int64) map[int64]bool {
	result := make(map[int64]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result
}

// TestHotBackfillFillsPageWhenLeadingClipsDeleted 验证窗口前若干条已删除时，
// 补取会跨轮向后取候选补满一页，且 HasMore 与游标能连续翻完全部有效内容。
func TestHotBackfillFillsPageWhenLeadingClipsDeleted(t *testing.T) {
	ctx := context.Background()
	// 6 条候选中前 3 条已删除，limit=2（count=3）需要两轮补取才能凑满。
	repo := &hotBackfillRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		allowed:      hotAllowed(3, 2, 1),
	}
	svc := appfeed.New(repo, appfeed.WithHotStrategy(&fakeHotCache{items: hotWindow(6)}))

	first, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "hot", Limit: 2})
	if err != nil {
		t.Fatalf("首页读取失败: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ClipID != 3 || first.Items[1].ClipID != 2 {
		t.Fatalf("补取后首页应返回 2 条有效内容 [3 2]，实际 %+v", first.Items)
	}
	if !first.HasMore {
		t.Fatal("补满一页且仍有剩余候选时 HasMore 必须为 true")
	}
	if first.NextCursor == "" {
		t.Fatal("补取补满一页后必须生成游标")
	}
	if len(repo.entryReqs) != 2 || len(repo.statsReqs) != 2 {
		t.Fatalf("首屏应经过两轮补取（每轮 1 次卡片 + 1 次计数），实际 entry=%d stats=%d",
			len(repo.entryReqs), len(repo.statsReqs))
	}

	// 续翻：游标锚定在最后返回的 2，之后只剩 1，理应返回短页并收尾。
	second, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "hot", Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("续翻失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ClipID != 1 {
		t.Fatalf("续翻应只返回未读过的 1，实际 %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("候选耗尽应返回短页且无下一页: hasMore=%v cursor=%q", second.HasMore, second.NextCursor)
	}
}

// TestHotBackfillKeepsCursorStrictlyDecreasing 验证删除条目穿插在窗口各处时，
// 连续翻页不重复、不跳条，顺序严格递减。
func TestHotBackfillKeepsCursorStrictlyDecreasing(t *testing.T) {
	ctx := context.Background()
	// 10 条候选中 10、9、5、2 已删除，limit=3（count=4）。
	repo := &hotBackfillRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		allowed:      hotAllowed(8, 7, 6, 4, 3, 1),
	}
	svc := appfeed.New(repo, appfeed.WithHotStrategy(&fakeHotCache{items: hotWindow(10)}))

	seen := make(map[int64]bool)
	var got []int64
	cursor := ""
	for page := 0; page < 6; page++ {
		result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "hot", Cursor: cursor, Limit: 3})
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
		if result.NextCursor == "" {
			t.Fatal("HasMore=true 时必须给出游标")
		}
		cursor = result.NextCursor
	}

	want := []int64{8, 7, 6, 4, 3, 1}
	if len(got) != len(want) {
		t.Fatalf("翻页结果 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("翻页结果 = %v, 期望 %v", got, want)
		}
	}
}

// TestHotBackfillShortPageWhenCandidatesExhausted 验证候选耗尽时如实返回短页：
// 无游标、HasMore=false，不制造「还有下一页」的假象。
func TestHotBackfillShortPageWhenCandidatesExhausted(t *testing.T) {
	ctx := context.Background()
	repo := &hotBackfillRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		allowed:      hotAllowed(4, 3, 2, 1),
	}
	svc := appfeed.New(repo, appfeed.WithHotStrategy(&fakeHotCache{items: hotWindow(4)}))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "hot", Limit: 5})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(result.Items) != 4 {
		t.Fatalf("候选耗尽应返回全部 4 条，实际 %d 条", len(result.Items))
	}
	if result.HasMore || result.NextCursor != "" {
		t.Fatalf("候选耗尽不应声明还有下一页: hasMore=%v cursor=%q", result.HasMore, result.NextCursor)
	}
}

// TestHotBackfillStopsAtRoundLimitWithCursor 验证补取轮数到上限但窗口仍有未检查候选时，
// 返回短页但必须给出前进的游标并声明 HasMore=true，避免分页提前结束。
func TestHotBackfillStopsAtRoundLimitWithCursor(t *testing.T) {
	ctx := context.Background()
	// 12 条候选只保留最后 1 条：3 轮最多检查 9 条，首屏无法补满。
	repo := &hotBackfillRepo{
		feedFakeRepo: newFeedFakeRepo(0),
		allowed:      hotAllowed(1),
	}
	svc := appfeed.New(repo, appfeed.WithHotStrategy(&fakeHotCache{items: hotWindow(12)}))

	first, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "hot", Limit: 2})
	if err != nil {
		t.Fatalf("首页读取失败: %v", err)
	}
	if len(first.Items) != 0 {
		t.Fatalf("前 9 条候选均已删除，首屏应为空页，实际 %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("轮数到上限但仍有候选时必须给出游标与 HasMore=true: hasMore=%v cursor=%q",
			first.HasMore, first.NextCursor)
	}
	if len(repo.entryReqs) != 3 {
		t.Fatalf("单请求补取轮数应受上限约束为 3 轮，实际 %d 轮", len(repo.entryReqs))
	}

	second, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "hot", Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("续翻失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ClipID != 1 {
		t.Fatalf("续翻应从锚点之后取到唯一的有效条目 1，实际 %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("候选耗尽后应正常收尾: hasMore=%v cursor=%q", second.HasMore, second.NextCursor)
	}
}
