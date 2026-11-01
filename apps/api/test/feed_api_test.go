// feed_api_test.go 用内存 fake 仓储验证 latest Feed 的分页契约：
// 3 条/页时首页 has_more=true，第二页取剩余且与首页无重复，非法场景返回 400。
package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
	httpfeed "pulsefeed/internal/interfaces/http/feed"

	"github.com/gin-gonic/gin"
)

// fakeFeedRepository 是内存版 feed 仓储，按 (published_at, id) 递减排序存储条目。
type fakeFeedRepository struct {
	entries []*domfeed.FeedEntry
}

// newFakeFeedRepository 构造 count 条发布时间互不相同（小时间隔）的条目。
func newFakeFeedRepository(count int) *fakeFeedRepository {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := make([]*domfeed.FeedEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, &domfeed.FeedEntry{
			ClipID:      int64(i),
			CreatorID:   1,
			Title:       fmt.Sprintf("clip-%d", i),
			VideoURL:    fmt.Sprintf("/uploads/video/%d.mp4", i),
			PublishedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].PublishedAt.Equal(entries[j].PublishedAt) {
			return entries[i].ClipID > entries[j].ClipID
		}
		return entries[i].PublishedAt.After(entries[j].PublishedAt)
	})
	return &fakeFeedRepository{entries: entries}
}

// ListLatestPage 按复合游标内存分页，语义与 GORM 实现一致。
func (f *fakeFeedRepository) ListLatestPage(_ context.Context, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	result := make([]*domfeed.FeedEntry, 0, limit)
	for _, entry := range f.entries {
		if cursor != nil {
			after := entry.PublishedAt.Before(cursor.PublishedAt) ||
				(entry.PublishedAt.Equal(cursor.PublishedAt) && entry.ClipID < cursor.ClipID)
			if !after {
				continue
			}
		}
		result = append(result, entry)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

// BatchGetEntries 返回空集，与 S7 的 GORM 空实现一致。
func (f *fakeFeedRepository) BatchGetEntries(_ context.Context, _ []int64) (map[int64]*domfeed.FeedEntry, error) {
	return map[int64]*domfeed.FeedEntry{}, nil
}

// BatchGetStats 返回空集，与 S7 的 GORM 空实现一致。
func (f *fakeFeedRepository) BatchGetStats(_ context.Context, _ []int64) (map[int64]*domfeed.EntryStats, error) {
	return map[int64]*domfeed.EntryStats{}, nil
}

// BatchGetViewerStates 返回空集，与 S7 的 GORM 空实现一致。
func (f *fakeFeedRepository) BatchGetViewerStates(_ context.Context, _ int64, _ []int64) (map[int64]*domfeed.ViewerState, error) {
	return map[int64]*domfeed.ViewerState{}, nil
}

// feedTestServer 装配内存 fake 的 Feed 端点。
func feedTestServer(repo domfeed.Repository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	handler := httpfeed.New(appfeed.New(repo))
	engine.GET("/api/feed", handler.GetFeed)
	return engine
}

// feedPage 是测试用的分页响应视图。
type feedPage struct {
	Items []struct {
		ClipID int64 `json:"clip_id"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// getFeedPage 请求一页 Feed 并解析信封，非 200 时返回 HTTP 状态码与空页。
func getFeedPage(t *testing.T, engine *gin.Engine, query string) (int, feedPage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/feed?"+query, nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	var resp struct {
		Code int      `json:"code"`
		Data feedPage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非合法 JSON: %v, body=%s", err, recorder.Body.String())
	}
	return recorder.Code, resp.Data
}

// TestFeedLatestPaginationCoversAllWithoutOverlap 验证 5 条数据按 3 条/页翻页：
// 首页 3 条且 has_more=true，第二页 2 条且 has_more=false，两页 clip_id 无交集。
func TestFeedLatestPaginationCoversAllWithoutOverlap(t *testing.T) {
	engine := feedTestServer(newFakeFeedRepository(5))

	status, page1 := getFeedPage(t, engine, "scene=latest&limit=3")
	if status != http.StatusOK {
		t.Fatalf("首页期望 200，实际 %d", status)
	}
	if len(page1.Items) != 3 {
		t.Fatalf("首页期望 3 条，实际 %d 条", len(page1.Items))
	}
	if !page1.HasMore {
		t.Fatalf("首页期望 has_more=true")
	}
	if page1.NextCursor == "" {
		t.Fatalf("首页期望非空 next_cursor")
	}

	status, page2 := getFeedPage(t, engine, "scene=latest&limit=3&cursor="+page1.NextCursor)
	if status != http.StatusOK {
		t.Fatalf("第二页期望 200，实际 %d", status)
	}
	if len(page2.Items) != 2 {
		t.Fatalf("第二页期望 2 条，实际 %d 条", len(page2.Items))
	}
	if page2.HasMore {
		t.Fatalf("第二页期望 has_more=false")
	}

	seen := map[int64]bool{}
	for _, item := range page1.Items {
		seen[item.ClipID] = true
	}
	for _, item := range page2.Items {
		if seen[item.ClipID] {
			t.Fatalf("两页 clip_id 存在重复: %d", item.ClipID)
		}
	}
	if len(seen)+len(page2.Items) != 5 {
		t.Fatalf("两页合计应覆盖全部 5 条，实际 %d 条", len(seen)+len(page2.Items))
	}
}

// TestFeedRejectsUnsupportedScene 验证非法场景返回 400。
func TestFeedRejectsUnsupportedScene(t *testing.T) {
	engine := feedTestServer(newFakeFeedRepository(5))
	status, _ := getFeedPage(t, engine, "scene=bogus")
	if status != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", status)
	}
}

// TestFeedRejectsInvalidCursor 验证非法游标返回 400。
func TestFeedRejectsInvalidCursor(t *testing.T) {
	engine := feedTestServer(newFakeFeedRepository(5))
	status, _ := getFeedPage(t, engine, "scene=latest&cursor=!!!not-base64!!!")
	if status != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", status)
	}
}
