// clip_fanout_worker_test.go 以内存 fake 验证发布扇出 Worker：阈值分流、500/批 inbox 写入、非法载荷直接确认与写失败触发重投。
package appclip_test

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

// fanoutWFakeRepo 是扇出用例的内存 feed 仓储，记录调用并支持错误注入。
type fanoutWFakeRepo struct {
	entries          map[int64]*domfeed.FeedEntry
	stats            map[int64]*domfeed.EntryStats
	followerCount    int64
	followerIDs      []int64
	entriesErr       error
	statsErr         error
	countErr         error
	followerIDsErr   error
	batchEntriesCall int
	batchStatsCall   int
	countCalls       int
	followerIDCalls  int
}

func (f *fanoutWFakeRepo) ListLatestPage(_ context.Context, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutWFakeRepo) ListFollowingPage(_ context.Context, _ int64, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutWFakeRepo) ListHotPage(_ context.Context, _ *domfeed.Cursor, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutWFakeRepo) ListFollowedCreators(_ context.Context, _ int64) ([]*domfeed.FollowedCreator, error) {
	return nil, nil
}

func (f *fanoutWFakeRepo) CountFollowers(_ context.Context, _ int64) (int64, error) {
	f.countCalls++
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.followerCount, nil
}

func (f *fanoutWFakeRepo) ListFollowerIDs(_ context.Context, _ int64) ([]int64, error) {
	f.followerIDCalls++
	if f.followerIDsErr != nil {
		return nil, f.followerIDsErr
	}
	return f.followerIDs, nil
}

func (f *fanoutWFakeRepo) ListCreatorRecentClips(_ context.Context, _ int64, _ int) ([]*domfeed.FeedEntry, error) {
	return nil, nil
}

func (f *fanoutWFakeRepo) ListLatestClipIDs(_ context.Context, _ int) ([]int64, error) {
	return nil, nil
}

func (f *fanoutWFakeRepo) BatchGetEntries(_ context.Context, clipIDs []int64) (map[int64]*domfeed.FeedEntry, error) {
	f.batchEntriesCall++
	if f.entriesErr != nil {
		return nil, f.entriesErr
	}
	result := make(map[int64]*domfeed.FeedEntry, len(clipIDs))
	for _, id := range clipIDs {
		if entry, ok := f.entries[id]; ok {
			result[id] = entry
		}
	}
	return result, nil
}

func (f *fanoutWFakeRepo) BatchGetStats(_ context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, error) {
	f.batchStatsCall++
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	result := make(map[int64]*domfeed.EntryStats, len(clipIDs))
	for _, id := range clipIDs {
		if stats, ok := f.stats[id]; ok {
			result[id] = stats
		}
	}
	return result, nil
}

func (f *fanoutWFakeRepo) BatchGetViewerStates(_ context.Context, _ int64, _ []int64) (map[int64]*domfeed.ViewerState, error) {
	return map[int64]*domfeed.ViewerState{}, nil
}

// fanoutWFakeCache 记录 inbox/outbox 写入与卡片/计数预热，支持写失败注入。
type fanoutWFakeCache struct {
	enabled      bool
	inbox        map[int64][]domfeed.InboxEntry
	outbox       map[int64][]domfeed.InboxEntry
	cards        map[int64]*domfeed.FeedEntry
	stats        map[int64]*domfeed.EntryStats
	inboxWrites  []int64
	outboxWrites []int64
	inboxCalls   int
	outboxCalls  int
	cardSetCalls int
	statSetCalls int
	addInboxErr  error
	addOutboxErr error
}

func fanoutWNewCache(enabled bool) *fanoutWFakeCache {
	return &fanoutWFakeCache{
		enabled: enabled,
		inbox:   make(map[int64][]domfeed.InboxEntry),
		outbox:  make(map[int64][]domfeed.InboxEntry),
		cards:   make(map[int64]*domfeed.FeedEntry),
		stats:   make(map[int64]*domfeed.EntryStats),
	}
}

func (f *fanoutWFakeCache) Enabled() bool { return f.enabled }

func (f *fanoutWFakeCache) AddInboxEntries(_ context.Context, userID int64, entries []domfeed.InboxEntry) error {
	f.inboxCalls++
	if f.addInboxErr != nil {
		return f.addInboxErr
	}
	f.inboxWrites = append(f.inboxWrites, userID)
	f.inbox[userID] = append(f.inbox[userID], entries...)
	return nil
}

func (f *fanoutWFakeCache) ListInbox(_ context.Context, userID int64) ([]domfeed.InboxEntry, error) {
	return f.inbox[userID], nil
}

func (f *fanoutWFakeCache) AddOutboxEntries(_ context.Context, creatorID int64, entries []domfeed.InboxEntry) error {
	f.outboxCalls++
	if f.addOutboxErr != nil {
		return f.addOutboxErr
	}
	f.outboxWrites = append(f.outboxWrites, creatorID)
	f.outbox[creatorID] = append(f.outbox[creatorID], entries...)
	return nil
}

func (f *fanoutWFakeCache) ListOutbox(_ context.Context, creatorID int64) ([]domfeed.InboxEntry, error) {
	return f.outbox[creatorID], nil
}

func (f *fanoutWFakeCache) GetCards(_ context.Context, clipIDs []int64) map[int64]*domfeed.FeedEntry {
	result := make(map[int64]*domfeed.FeedEntry, len(clipIDs))
	for _, id := range clipIDs {
		if entry, ok := f.cards[id]; ok {
			result[id] = entry
		}
	}
	return result
}

func (f *fanoutWFakeCache) SetCards(_ context.Context, entries []*domfeed.FeedEntry) {
	f.cardSetCalls += len(entries)
	for _, entry := range entries {
		f.cards[entry.ClipID] = entry
	}
}

func (f *fanoutWFakeCache) GetStats(_ context.Context, clipIDs []int64) map[int64]*domfeed.EntryStats {
	result := make(map[int64]*domfeed.EntryStats, len(clipIDs))
	for _, id := range clipIDs {
		if stats, ok := f.stats[id]; ok {
			result[id] = stats
		}
	}
	return result
}

func (f *fanoutWFakeCache) SetStats(_ context.Context, stats map[int64]*domfeed.EntryStats) {
	f.statSetCalls += len(stats)
	for id, value := range stats {
		f.stats[id] = value
	}
}

// fanoutWNewFixture 构造 clip_id=42、creator_id=7 的 Worker 测试夹具。
func fanoutWNewFixture() (*fanoutWFakeRepo, *fanoutWFakeCache, []byte, time.Time) {
	publishedAt := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	repo := &fanoutWFakeRepo{
		entries: map[int64]*domfeed.FeedEntry{
			42: {
				ClipID:      42,
				CreatorID:   7,
				Title:       "扇出条目",
				PublishedAt: publishedAt,
			},
		},
		stats: map[int64]*domfeed.EntryStats{
			42: {LikeCount: 3, SaveCount: 1},
		},
	}
	cache := fanoutWNewCache(true)
	body, _ := json.Marshal(appclip.ClipPublished{ClipID: 42, CreatorID: 7, PublishedAt: publishedAt})
	return repo, cache, body, publishedAt
}

// TestFanoutWorkerDeadLettersInvalidPayload 验证非法 JSON 与非法字段标记为不可重试（转入死信），不做任何缓存写。
func TestFanoutWorkerDeadLettersInvalidPayload(t *testing.T) {
	bodies := [][]byte{
		[]byte("not-json"),
		[]byte(`{"clip_id":0,"creator_id":7}`),
		[]byte(`{"clip_id":42}`),
		[]byte(`{"clip_id":-5,"creator_id":7}`),
	}
	for _, body := range bodies {
		repo, cache, _, _ := fanoutWNewFixture()
		worker := appclip.NewFanoutWorker(repo, cache)

		if err := worker.Handle(context.Background(), body); !messaging.IsPermanent(err) {
			t.Fatalf("非法载荷 %s 应标记不可重试（转入死信），实际 %v", body, err)
		}
		if len(cache.inboxWrites) != 0 || len(cache.outboxWrites) != 0 || cache.cardSetCalls != 0 {
			t.Fatalf("非法载荷 %s 不应触发缓存写", body)
		}
		if repo.batchEntriesCall != 0 || repo.countCalls != 0 {
			t.Fatalf("非法载荷 %s 不应回源仓储", body)
		}
	}
}

// TestFanoutWorkerSkipsWhenCacheDisabled 验证缓存未启用时事件直接确认，读路径回源 DB。
func TestFanoutWorkerSkipsWhenCacheDisabled(t *testing.T) {
	repo, cache, body, _ := fanoutWNewFixture()
	cache.enabled = false
	repo.followerCount = 3
	repo.followerIDs = []int64{11, 12, 13}

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("缓存禁用时不应返回错误: %v", err)
	}
	if len(cache.inboxWrites) != 0 || len(cache.outboxWrites) != 0 || cache.cardSetCalls != 0 {
		t.Fatalf("缓存禁用时不应触发任何写")
	}
	if repo.batchEntriesCall != 0 || repo.countCalls != 0 {
		t.Fatalf("缓存禁用时不应回源仓储")
	}
}

// TestFanoutWorkerDeadLettersInvisibleClip 验证 clip 已删除/不可见时标记不可重试（转入死信），不写缓存也不再读粉丝。
func TestFanoutWorkerDeadLettersInvisibleClip(t *testing.T) {
	repo, cache, _, _ := fanoutWNewFixture()
	body, _ := json.Marshal(appclip.ClipPublished{ClipID: 999, CreatorID: 7, PublishedAt: time.Now()})

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); !messaging.IsPermanent(err) {
		t.Fatalf("不可见 clip 应标记不可重试（转入死信），实际 %v", err)
	}
	if len(cache.inboxWrites) != 0 || len(cache.outboxWrites) != 0 || cache.cardSetCalls != 0 {
		t.Fatalf("不可见 clip 不应触发缓存写")
	}
	if repo.batchStatsCall != 0 || repo.countCalls != 0 {
		t.Fatalf("不可见 clip 不应继续读取计数/粉丝数")
	}
}

// TestFanoutWorkerFansOutInboxInBatches 验证普通创作者按 500/批推入全部粉丝 inbox，不漏写不重复。
func TestFanoutWorkerFansOutInboxInBatches(t *testing.T) {
	repo, cache, body, publishedAt := fanoutWNewFixture()
	repo.followerCount = 1200
	ids := make([]int64, 1200)
	for i := range ids {
		ids[i] = int64(1000 + i)
	}
	repo.followerIDs = ids

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("inbox 扇出不应失败: %v", err)
	}
	if cache.inboxCalls != 1200 || len(cache.inboxWrites) != 1200 {
		t.Fatalf("inbox 写入次数 = %d/%d, 期望 1200", cache.inboxCalls, len(cache.inboxWrites))
	}
	if len(cache.outboxWrites) != 0 {
		t.Fatalf("普通创作者不应写 outbox, 实际 %d 次", len(cache.outboxWrites))
	}
	if repo.followerIDCalls != 1 || repo.countCalls != 1 {
		t.Fatalf("仓储调用次数不正确: followerIDs=%d count=%d", repo.followerIDCalls, repo.countCalls)
	}

	seen := make(map[int64]bool, 1200)
	for i, userID := range cache.inboxWrites {
		if userID != ids[i] {
			t.Fatalf("第 %d 个 inbox 写入 userID = %d, 期望 %d（分批顺序被破坏）", i, userID, ids[i])
		}
		if seen[userID] {
			t.Fatalf("userID %d 被重复写入", userID)
		}
		seen[userID] = true
		entries := cache.inbox[userID]
		if len(entries) != 1 {
			t.Fatalf("userID %d 的 inbox 条目数 = %d, 期望 1", userID, len(entries))
		}
		entry := entries[0]
		if entry.ClipID != 42 || entry.CreatorID != 7 || !entry.PublishedAt.Equal(publishedAt) {
			t.Fatalf("userID %d 的 inbox 条目不正确: %+v", userID, entry)
		}
	}

	if _, ok := cache.cards[42]; !ok {
		t.Fatalf("卡片缓存未被预热")
	}
	if cache.statSetCalls != 1 {
		t.Fatalf("计数缓存预热次数 = %d, 期望 1", cache.statSetCalls)
	}
	if stats := cache.stats[42]; stats == nil || stats.LikeCount != 3 {
		t.Fatalf("计数缓存内容不正确: %+v", stats)
	}
}

// TestFanoutWorkerUsesOutboxAtBigVThreshold 验证粉丝数达到阈值只写创作者 outbox，不展开粉丝列表。
func TestFanoutWorkerUsesOutboxAtBigVThreshold(t *testing.T) {
	repo, cache, body, publishedAt := fanoutWNewFixture()
	repo.followerCount = appfeed.BigVFollowerThreshold
	repo.followerIDs = []int64{11, 12, 13}

	worker := appclip.NewFanoutWorker(repo, cache)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("outbox 扇出不应失败: %v", err)
	}
	if len(cache.outboxWrites) != 1 || cache.outboxWrites[0] != 7 {
		t.Fatalf("outbox 写入 = %v, 期望 [7]", cache.outboxWrites)
	}
	if len(cache.inboxWrites) != 0 {
		t.Fatalf("大 V 不应写粉丝 inbox, 实际 %d 次", len(cache.inboxWrites))
	}
	if repo.followerIDCalls != 0 {
		t.Fatalf("大 V 路径不应读取粉丝列表, 实际 %d 次", repo.followerIDCalls)
	}
	if repo.countCalls != 1 {
		t.Fatalf("应读取一次粉丝数, 实际 %d 次", repo.countCalls)
	}
	if cache.cardSetCalls != 1 || cache.statSetCalls != 1 {
		t.Fatalf("缓存预热次数不正确: cards=%d stats=%d", cache.cardSetCalls, cache.statSetCalls)
	}

	entries := cache.outbox[7]
	if len(entries) != 1 {
		t.Fatalf("创作者 outbox 条目数 = %d, 期望 1", len(entries))
	}
	if entries[0].ClipID != 42 || entries[0].CreatorID != 7 || !entries[0].PublishedAt.Equal(publishedAt) {
		t.Fatalf("outbox 条目不正确: %+v", entries[0])
	}
}

// TestFanoutWorkerRetriesOnInboxWriteFailure 验证粉丝 inbox 写失败返回可重投的错误（保留原因）。
func TestFanoutWorkerRetriesOnInboxWriteFailure(t *testing.T) {
	repo, cache, body, _ := fanoutWNewFixture()
	cacheErr := errors.New("inbox cache unavailable")
	cache.addInboxErr = cacheErr
	repo.followerCount = 2
	repo.followerIDs = []int64{11, 12}

	worker := appclip.NewFanoutWorker(repo, cache)
	err := worker.Handle(context.Background(), body)
	if err == nil {
		t.Fatalf("inbox 写失败应返回错误触发重投")
	}
	if !errors.Is(err, cacheErr) {
		t.Fatalf("错误应保留底层原因, 实际 %v", err)
	}
	if len(cache.outboxWrites) != 0 {
		t.Fatalf("inbox 失败路径不应写 outbox")
	}
}

// TestFanoutWorkerRetriesOnOutboxWriteFailure 验证大 V outbox 写失败同样返回错误触发重投。
func TestFanoutWorkerRetriesOnOutboxWriteFailure(t *testing.T) {
	repo, cache, body, _ := fanoutWNewFixture()
	cacheErr := errors.New("outbox cache unavailable")
	cache.addOutboxErr = cacheErr
	repo.followerCount = appfeed.BigVFollowerThreshold

	worker := appclip.NewFanoutWorker(repo, cache)
	err := worker.Handle(context.Background(), body)
	if err == nil {
		t.Fatalf("outbox 写失败应返回错误触发重投")
	}
	if !errors.Is(err, cacheErr) {
		t.Fatalf("错误应保留底层原因, 实际 %v", err)
	}
	if len(cache.inboxWrites) != 0 {
		t.Fatalf("outbox 失败路径不应写 inbox")
	}
}

// TestFanoutWorkerReturnsErrorOnSourceFailure 验证回源条目/计数/粉丝数/粉丝列表失败均上抛错误。
func TestFanoutWorkerReturnsErrorOnSourceFailure(t *testing.T) {
	ctx := context.Background()
	sourceErr := errors.New("source unavailable")

	tests := []struct {
		name   string
		mutate func(*fanoutWFakeRepo)
	}{
		{name: "条目回源失败", mutate: func(r *fanoutWFakeRepo) { r.entriesErr = sourceErr }},
		{name: "计数回源失败", mutate: func(r *fanoutWFakeRepo) { r.statsErr = sourceErr }},
		{name: "粉丝数读取失败", mutate: func(r *fanoutWFakeRepo) { r.countErr = sourceErr }},
		{name: "粉丝列表读取失败", mutate: func(r *fanoutWFakeRepo) {
			r.followerCount = 1
			r.followerIDsErr = sourceErr
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cache, body, _ := fanoutWNewFixture()
			tt.mutate(repo)

			worker := appclip.NewFanoutWorker(repo, cache)
			err := worker.Handle(ctx, body)
			if err == nil {
				t.Fatalf("%s 应返回错误交由 MQ 重投", tt.name)
			}
			if !errors.Is(err, sourceErr) {
				t.Fatalf("错误应保留底层原因, 实际 %v", err)
			}
		})
	}
}

// TestFanoutWorkerReportsName 验证 Worker 名称用于启动日志区分。
func TestFanoutWorkerReportsName(t *testing.T) {
	repo, cache, _, _ := fanoutWNewFixture()
	if name := appclip.NewFanoutWorker(repo, cache).Name(); name != "clip_fanout" {
		t.Fatalf("Worker 名称 = %q, 期望 clip_fanout", name)
	}
}
