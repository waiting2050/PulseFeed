// following_inbox_test.go 验证 inbox 已取关残留的补取上限，
// 以及取关清理后读侧不再为空过滤而反复空转。
package appfeed_test

import (
	"context"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// residueInboxCache 模拟 inbox 中堆积大量指定创作者条目，并按游标/limit 真实分页，
// 便于精确断言补取轮数与停止位置。
type residueInboxCache struct {
	*followingFaultCache
	entries []domfeed.InboxEntry
	calls   int
}

func (c *residueInboxCache) ListInbox(_ context.Context, _ int64, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error) {
	c.calls++
	page := make([]domfeed.InboxEntry, 0, limit)
	for _, entry := range c.entries {
		if cursor != nil {
			after := entry.PublishedAt.Before(cursor.PublishedAt) ||
				(entry.PublishedAt.Equal(cursor.PublishedAt) && entry.ClipID < cursor.ClipID)
			if !after {
				continue
			}
		}
		page = append(page, entry)
		if len(page) == limit {
			break
		}
	}
	return page, nil
}

// newResidueInboxCache 构造 count 条按发布时间递减、同属 creatorID 的 inbox 条目。
func newResidueInboxCache(count int, creatorID int64) *residueInboxCache {
	base := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	entries := make([]domfeed.InboxEntry, 0, count)
	for i := count; i >= 1; i-- {
		entries = append(entries, domfeed.InboxEntry{
			ClipID:      int64(10000 + i),
			CreatorID:   creatorID,
			PublishedAt: base.Add(time.Duration(i) * time.Millisecond),
		})
	}
	return &residueInboxCache{followingFaultCache: &followingFaultCache{}, entries: entries}
}

// TestFollowingInboxRefillStopsAtRoundCap 验证 inbox 全是已取关残留时，
// 补取最多 3 轮（约 63 条）后停止，不再无限空转，缺口交给 DB 回源补齐。
func TestFollowingInboxRefillStopsAtRoundCap(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(40)
	repo.creators = []*domfeed.FollowedCreator{{CreatorID: 7}}
	// 200 条残留属于已取关的创作者 99（repo 只关注 7）。
	cache := newResidueInboxCache(200, 99)
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20})
	if err != nil {
		t.Fatalf("关注流读取失败: %v", err)
	}
	if cache.calls != 3 {
		t.Fatalf("补取应在上限 3 轮后停止，实际 %d 轮", cache.calls)
	}
	if repo.pageCalls != 1 {
		t.Fatalf("补取耗尽后应交给 DB 回源一次，实际 %d 次", repo.pageCalls)
	}
	if len(result.Items) != 20 {
		t.Fatalf("DB 回源应补齐一页，实际 %d 条", len(result.Items))
	}
}

// TestFollowingInboxStopsRefillingAfterUnfollowCleanup 验证取关清理后 inbox 只剩仍关注的创作者，
// 补取在第一轮就取满，不再产生额外的空转读取。
func TestFollowingInboxStopsRefillingAfterUnfollowCleanup(t *testing.T) {
	ctx := context.Background()
	repo := newFallbackFollowingRepo(40)
	repo.creators = []*domfeed.FollowedCreator{{CreatorID: 7}}
	cache := newResidueInboxCache(200, 7)
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))

	if _, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20}); err != nil {
		t.Fatalf("关注流读取失败: %v", err)
	}
	if cache.calls != 1 {
		t.Fatalf("inbox 无已取关残留时应一轮取满，实际读取 %d 轮", cache.calls)
	}
}
