//go:build integration

// pagination_integration_test.go 覆盖 latest 场景的游标分页边界：
// 逐页翻到 next_cursor 为空，校验每页条数、总数、无重复无遗漏，并让页边界跨分钟。
// 说明：跨分钟游标取 latest 场景（按 published_at, id 递减）；for-you 的游标还携带 snapshot_at
// 与推荐分数，不属于本次去重/游标边界问题的验证范围。
package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
	infrafeed "pulsefeed/internal/infra/persistence/feed"
)

func TestLatestPaginationCursorBoundary(t *testing.T) {
	ctx := context.Background()
	// latest 是全站共享流：用未来时间基准把本用例的 7 条放到最前面，
	// 避免其他用例残留的 clip 干扰页数与顺序断言。
	base := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	offsets := []time.Duration{
		0, 30 * time.Second, 1 * time.Minute, 90 * time.Second,
		2 * time.Minute, 150 * time.Second, 3 * time.Minute,
	}

	// 按时间递增写入 7 条可见 clip，期望读取顺序为倒序。
	idsAsc := make([]int64, 0, len(offsets))
	for i, offset := range offsets {
		idsAsc = append(idsAsc, seedClip(t, 9002, fmt.Sprintf("it-page-%02d", i), base.Add(offset), 1))
	}
	// 一条时间更新但已删除的 clip，必须被分页排除。
	seedClip(t, 9002, "it-page-deleted", base.Add(4*time.Minute), 2)

	expected := make([]int64, 0, len(idsAsc))
	for i := len(idsAsc) - 1; i >= 0; i-- {
		expected = append(expected, idsAsc[i])
	}

	svc := appfeed.New(infrafeed.New(testDB))
	const limit = 3
	cursor := ""
	collected := make([]int64, 0, len(expected))
	seen := make(map[int64]bool, len(expected))
	pageSizes := make([]int, 0, 4)
	crossedMinute := false
	var prevLastMinute int = -1
	page := 0

	for len(collected) < len(expected) {
		page++
		result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{
			Scene:  string(domfeed.SceneLatest),
			Limit:  limit,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("第 %d 页读取失败: %v", page, err)
		}
		pageSizes = append(pageSizes, len(result.Items))

		pageIDs := make([]int64, 0, len(result.Items))
		for _, item := range result.Items {
			if seen[item.ClipID] {
				t.Fatalf("第 %d 页出现重复 clip %d", page, item.ClipID)
			}
			seen[item.ClipID] = true
			collected = append(collected, item.ClipID)
			pageIDs = append(pageIDs, item.ClipID)
		}
		if len(result.Items) > 0 && prevLastMinute >= 0 && result.Items[0].PublishedAt.Minute() != prevLastMinute {
			crossedMinute = true
		}
		if len(result.Items) > 0 {
			prevLastMinute = result.Items[len(result.Items)-1].PublishedAt.Minute()
		}

		t.Logf("page=%d count=%d has_more=%v next_cursor=%q ids=%v", page, len(result.Items), result.HasMore, result.NextCursor, pageIDs)

		// 本用例的 7 条已全部读完即可结束：latest 上仍可能有其他用例的数据，
		// 不能把“全站末页”当作本用例的终态。
		if len(collected) >= len(expected) {
			break
		}
		if !result.HasMore {
			t.Fatalf("尚未读完 %d 条便无更多数据，collected=%v", len(expected), collected)
		}
		if result.NextCursor == "" {
			t.Fatalf("has_more=true 的页必须返回 next_cursor")
		}
		if page > 5 {
			t.Fatalf("分页循环超过预期页数，可能存在游标不前进")
		}
		cursor = result.NextCursor
	}

	// 最后一页可能顺带返回其他用例更旧的数据；只校验本用例的 7 条前缀。
	if len(collected) > len(expected) {
		collected = collected[:len(expected)]
	}
	if len(collected) != len(expected) {
		t.Fatalf("总条数应为 %d，实际 %d（collected=%v）", len(expected), len(collected), collected)
	}
	for i := range expected {
		if collected[i] != expected[i] {
			t.Fatalf("第 %d 条顺序错误：期望 %d，实际 %d（collected=%v）", i, expected[i], collected[i], collected)
		}
	}
	// 前两页必须满页；第 3 页至少含本用例最后 1 条，其后可能混入其他用例数据。
	if len(pageSizes) != 3 || pageSizes[0] != limit || pageSizes[1] != limit || pageSizes[2] < 1 {
		t.Fatalf("每页条数不符合预期（前两页满页、第 3 页至少 1 条），实际 %v", pageSizes)
	}
	if !crossedMinute {
		t.Fatalf("页边界未跨分钟，测试数据未覆盖游标跨分钟场景")
	}

	t.Logf("分页验收通过：页数=%d 每页条数=%v 总数=%d，且页边界跨分钟", page, pageSizes, len(collected))
}
