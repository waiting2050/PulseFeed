// clip_fanout_test.go 用内存 fake 验证 FanoutWorker 的核心契约：
// 粉丝数达到大 V 阈值走 outbox，低于阈值按 500/批推入全部粉丝 inbox，
// 非法载荷/缓存禁用/clip 不可见直接确认，缓存写失败返回错误触发重投。
package test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appclip "pulsefeed/internal/application/clip"
	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
	"pulsefeed/internal/domain/messaging"
)

// fanoutFakeRepository 是扇出测试用的内存 feed 仓储，仅实现 Worker 消费的方法。
type fanoutFakeRepository struct {
	entries       map[int64]*domfeed.FeedEntry
	stats         map[int64]*domfeed.EntryStats
	followerCount int64
	followerIDs   []int64
}

func (f *fanoutFakeRepository) ListLatestPage(_ context.Context, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutFakeRepository) ListFollowingPage(_ context.Context, _ int64, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutFakeRepository) ListHotPage(_ context.Context, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutFakeRepository) ListFollowedCreators(_ context.Context, _ int64) ([]*domfeed.FollowedCreator, error) {
	return nil, nil
}

func (f *fanoutFakeRepository) CountFollowers(_ context.Context, _ int64) (int64, error) {
	return f.followerCount, nil
}

func (f *fanoutFakeRepository) ListFollowerIDs(_ context.Context, _ int64) ([]int64, error) {
	return f.followerIDs, nil
}

func (f *fanoutFakeRepository) ListCreatorRecentClips(_ context.Context, _ int64, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutFakeRepository) ListLatestClipMedia(_ context.Context, _ int) ([]domfeed.PreloadClip, error) {
	return nil, nil
}

func (f *fanoutFakeRepository) BatchGetEntries(_ context.Context, ids []int64) (map[int64]*domfeed.FeedEntry, error) {
	result := make(map[int64]*domfeed.FeedEntry, len(ids))
	for _, id := range ids {
		if entry, ok := f.entries[id]; ok {
			result[id] = entry
		}
	}
	return result, nil
}

func (f *fanoutFakeRepository) BatchGetStats(_ context.Context, ids []int64) (map[int64]*domfeed.EntryStats, error) {
	result := make(map[int64]*domfeed.EntryStats, len(ids))
	for _, id := range ids {
		if stat, ok := f.stats[id]; ok {
			result[id] = stat
		}
	}
	return result, nil
}

func (f *fanoutFakeRepository) BatchGetCreatorProfiles(_ context.Context, _ []int64) (map[int64]*domfeed.CreatorProfile, error) {
	return map[int64]*domfeed.CreatorProfile{}, nil
}

func (f *fanoutFakeRepository) BatchGetFollowedCreatorIDs(_ context.Context, _ int64, _ []int64) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}

func (f *fanoutFakeRepository) BatchGetViewerStates(_ context.Context, _ int64, _ []int64) (map[int64]*domfeed.ViewerState, error) {
	return map[int64]*domfeed.ViewerState{}, nil
}

// fanoutFakeCache 记录 inbox/outbox 写入与预热调用；inboxErr 用于模拟缓存写失败。
type fanoutFakeCache struct {
	enabled     bool
	inboxCalls  []int64
	outboxCalls []int64
	cardsSet    int
	statsSet    int
	inboxErr    error
}

func (f *fanoutFakeCache) Enabled() bool { return f.enabled }

func (f *fanoutFakeCache) AddInboxEntries(_ context.Context, userID int64, _ []domfeed.InboxEntry) error {
	if f.inboxErr != nil {
		return f.inboxErr
	}
	f.inboxCalls = append(f.inboxCalls, userID)
	return nil
}

func (f *fanoutFakeCache) ListInbox(_ context.Context, _ int64) ([]domfeed.InboxEntry, error) {
	return nil, nil
}

func (f *fanoutFakeCache) AddOutboxEntries(_ context.Context, creatorID int64, _ []domfeed.InboxEntry) error {
	f.outboxCalls = append(f.outboxCalls, creatorID)
	return nil
}

func (f *fanoutFakeCache) ListOutbox(_ context.Context, _ int64) ([]domfeed.InboxEntry, error) {
	return nil, nil
}

func (f *fanoutFakeCache) GetCards(_ context.Context, _ []int64) map[int64]*domfeed.FeedEntry {
	return map[int64]*domfeed.FeedEntry{}
}

func (f *fanoutFakeCache) SetCards(_ context.Context, entries []*domfeed.FeedEntry) {
	f.cardsSet += len(entries)
}

func (f *fanoutFakeCache) GetStats(_ context.Context, _ []int64) map[int64]*domfeed.EntryStats {
	return map[int64]*domfeed.EntryStats{}
}

func (f *fanoutFakeCache) SetStats(_ context.Context, stats map[int64]*domfeed.EntryStats) {
	f.statsSet += len(stats)
}

// newFanoutFixture 构造 clip_id=42、creator_id=7 的扇出测试夹具。
func newFanoutFixture() (*fanoutFakeRepository, *fanoutFakeCache, []byte) {
	publishedAt := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	repo := &fanoutFakeRepository{
		entries: map[int64]*domfeed.FeedEntry{
			42: {ClipID: 42, CreatorID: 7, Title: "fanout", VideoURL: "/v/42.mp4", PublishedAt: publishedAt},
		},
		stats: map[int64]*domfeed.EntryStats{
			42: {},
		},
	}
	cache := &fanoutFakeCache{enabled: true}
	body, _ := json.Marshal(appclip.ClipPublished{ClipID: 42, CreatorID: 7, PublishedAt: publishedAt})
	return repo, cache, body
}

// TestFanoutInboxPathBelowThreshold 验证粉丝数低于阈值时推模型扇出：
// 每个粉丝 inbox 恰好收到一次写入，outbox 不触发，卡片/计数缓存被预热。
func TestFanoutInboxPathBelowThreshold(t *testing.T) {
	repo, cache, body := newFanoutFixture()
	repo.followerCount = 3
	repo.followerIDs = []int64{11, 12, 13}

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(cache.inboxCalls) != 3 {
		t.Fatalf("inbox 写入次数 = %d, 期望 3", len(cache.inboxCalls))
	}
	for i, id := range []int64{11, 12, 13} {
		if cache.inboxCalls[i] != id {
			t.Fatalf("inbox 第 %d 个写入 userID = %d, 期望 %d", i, cache.inboxCalls[i], id)
		}
	}
	if len(cache.outboxCalls) != 0 {
		t.Fatalf("普通创作者不应写 outbox, 实际 %d 次", len(cache.outboxCalls))
	}
	if cache.cardsSet != 1 || cache.statsSet != 1 {
		t.Fatalf("缓存预热次数 cards=%d stats=%d, 期望各 1", cache.cardsSet, cache.statsSet)
	}
}

// TestFanoutOutboxPathAtThreshold 验证粉丝数达到大 V 阈值时走拉模型：只写创作者 outbox。
func TestFanoutOutboxPathAtThreshold(t *testing.T) {
	repo, cache, body := newFanoutFixture()
	repo.followerCount = appfeed.BigVFollowerThreshold

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(cache.outboxCalls) != 1 || cache.outboxCalls[0] != 7 {
		t.Fatalf("outbox 写入 = %v, 期望 [7]", cache.outboxCalls)
	}
	if len(cache.inboxCalls) != 0 {
		t.Fatalf("大 V 不应写粉丝 inbox, 实际 %d 次", len(cache.inboxCalls))
	}
}

// TestFanoutInboxBatching 验证 500/批切分：1200 个粉丝全部收到且仅收到一次写入、
// 顺序与输入一致（顺序保持证明按 500+500+200 连续分块，无跨块遗漏或重复）。
func TestFanoutInboxBatching(t *testing.T) {
	repo, cache, body := newFanoutFixture()
	repo.followerCount = 1200
	ids := make([]int64, 1200)
	for i := range ids {
		ids[i] = int64(1000 + i)
	}
	repo.followerIDs = ids

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(cache.inboxCalls) != 1200 {
		t.Fatalf("inbox 写入次数 = %d, 期望 1200", len(cache.inboxCalls))
	}
	seen := make(map[int64]bool, 1200)
	for i, userID := range cache.inboxCalls {
		if userID != ids[i] {
			t.Fatalf("第 %d 个写入 userID = %d, 期望 %d（分块顺序被破坏）", i, userID, ids[i])
		}
		if seen[userID] {
			t.Fatalf("userID %d 被重复写入", userID)
		}
		seen[userID] = true
	}
}

// TestFanoutDeadLettersInvalidPayload 验证非法载荷与非法字段标记为不可重试（转入死信），不触发任何缓存写。
func TestFanoutDeadLettersInvalidPayload(t *testing.T) {
	repo, cache, _ := newFanoutFixture()
	worker := appclip.NewFanoutWorker(repo, cache)

	if err := worker.Handle(context.Background(), []byte("not-json")); !messaging.IsPermanent(err) {
		t.Fatalf("非法 JSON 应标记不可重试，实际 %v", err)
	}
	badBody, _ := json.Marshal(appclip.ClipPublished{ClipID: 0, CreatorID: 7})
	if err := worker.Handle(context.Background(), badBody); !messaging.IsPermanent(err) {
		t.Fatalf("非法字段应标记不可重试，实际 %v", err)
	}
	if len(cache.inboxCalls) != 0 || len(cache.outboxCalls) != 0 || cache.cardsSet != 0 {
		t.Fatalf("非法载荷不应触发缓存写")
	}
}

// TestFanoutSkipsWhenCacheDisabled 验证缓存未启用时事件直接确认（读路径回源 DB）。
func TestFanoutSkipsWhenCacheDisabled(t *testing.T) {
	repo, cache, body := newFanoutFixture()
	cache.enabled = false
	repo.followerCount = 3
	repo.followerIDs = []int64{11, 12, 13}

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if len(cache.inboxCalls) != 0 || len(cache.outboxCalls) != 0 || cache.cardsSet != 0 {
		t.Fatalf("缓存禁用不应触发任何写")
	}
}

// TestFanoutDeadLettersInvisibleClip 验证 clip 已删除时标记不可重试（转入死信），不写缓存。
func TestFanoutDeadLettersInvisibleClip(t *testing.T) {
	repo, cache, _ := newFanoutFixture()
	body, _ := json.Marshal(appclip.ClipPublished{ClipID: 999, CreatorID: 7, PublishedAt: time.Now()})

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); !messaging.IsPermanent(err) {
		t.Fatalf("不可见 clip 应标记不可重试（转入死信），实际 %v", err)
	}
	if len(cache.inboxCalls) != 0 || len(cache.outboxCalls) != 0 || cache.cardsSet != 0 {
		t.Fatalf("不可见 clip 不应触发缓存写")
	}
}

// TestFanoutRetriesOnCacheError 验证 inbox 写失败返回错误，交由 MQ 重新入队（ZADD 幂等）。
func TestFanoutRetriesOnCacheError(t *testing.T) {
	repo, cache, body := newFanoutFixture()
	repo.followerCount = 2
	repo.followerIDs = []int64{11, 12}
	cache.inboxErr = errors.New("redis down")

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err == nil {
		t.Fatalf("缓存写失败应返回错误触发重投")
	}
}
