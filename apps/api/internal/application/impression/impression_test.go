// impression_test.go 用内存 fake 验证播放事件上报的 event_id 幂等、播放/完播累加、参数校验、
// 投递失败不影响上报，以及 PlayEventWorker 对已同步事件的幂等与未同步事件的曝光补偿契约。
package appimpression_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	appimpression "pulsefeed/internal/application/impression"
	domimpression "pulsefeed/internal/domain/impression"
	"pulsefeed/internal/domain/messaging"
)

// impressionFakeRepository 是曝光仓储的内存替身：按 event_id 去重，并按 (viewer, clip, day) 聚合计数。
type impressionFakeRepository struct {
	clips       map[int64]bool
	events      map[string]*domimpression.PlayEvent
	impressions map[string]*domimpression.Impression
	synced      map[string]bool
	recordCalls int
	upsertCalls int
	markCalls   int
	syncedCalls int
	lastIsNew   bool
	recordErr   error
	upsertErr   error
	syncedErr   error
	markErr     error
}

var _ domimpression.Repository = (*impressionFakeRepository)(nil)

// newImpressionFakeRepository 创建内存仓储，clipIDs 为可播放的 clip 集合。
func newImpressionFakeRepository(clipIDs ...int64) *impressionFakeRepository {
	clips := make(map[int64]bool, len(clipIDs))
	for _, clipID := range clipIDs {
		clips[clipID] = true
	}
	return &impressionFakeRepository{
		clips:       clips,
		events:      make(map[string]*domimpression.PlayEvent),
		impressions: make(map[string]*domimpression.Impression),
		synced:      make(map[string]bool),
	}
}

// impressionFakeKey 生成曝光聚合行的内存键。
func impressionFakeKey(viewerID, clipID int64, day string) string {
	return fmt.Sprintf("%d|%d|%s", viewerID, clipID, day)
}

// applyImpression 按事件类型把一条事件的贡献累加进当日聚合行：
// 只有 play 累加 play_count，只有 complete（或带完成标记的 play）累加 completed_count。
func (f *impressionFakeRepository) applyImpression(event *domimpression.PlayEvent) {
	day := domimpression.Day(event.CreatedAt)
	key := impressionFakeKey(event.ViewerID, event.ClipID, day)
	row, ok := f.impressions[key]
	if !ok {
		row = &domimpression.Impression{
			ViewerID:      event.ViewerID,
			ClipID:        event.ClipID,
			Day:           day,
			FirstPlayedAt: event.CreatedAt,
			LastPlayedAt:  event.CreatedAt,
		}
		f.impressions[key] = row
	}
	switch event.EventType {
	case domimpression.EventTypePlay:
		row.PlayCount++
		if event.Completed {
			row.CompletedCount++
		}
	case domimpression.EventTypeComplete:
		row.CompletedCount++
	}
	if event.CreatedAt.After(row.LastPlayedAt) {
		row.LastPlayedAt = event.CreatedAt
	}
	if event.CreatedAt.Before(row.FirstPlayedAt) {
		row.FirstPlayedAt = event.CreatedAt
	}
}

// playEventInput 构造一条默认归属读者 7、clip 10 的事件入参，便于测试按类型覆盖。
func playEventInput(eventID string, clipID, watchMs int64, eventType string, completed bool) domimpression.PlayEventInput {
	return domimpression.PlayEventInput{
		EventID:    eventID,
		ViewerID:   7,
		ClipID:     clipID,
		EventType:  eventType,
		Scene:      "latest",
		RequestID:  "req-1",
		WatchMs:    watchMs,
		Completed:  completed,
		Impression: true,
	}
}

// RecordPlayEvent 模拟唯一键幂等落库：仅首次写入时聚合曝光并标记已同步。
func (f *impressionFakeRepository) RecordPlayEvent(_ context.Context, event *domimpression.PlayEvent) (bool, error) {
	f.recordCalls++
	f.lastIsNew = false
	if f.recordErr != nil {
		return false, f.recordErr
	}
	if !f.clips[event.ClipID] {
		return false, domimpression.ErrClipNotFound
	}
	if _, exists := f.events[event.EventID]; exists {
		return false, nil
	}

	clone := *event
	f.events[event.EventID] = &clone
	f.applyImpression(event)
	f.synced[event.EventID] = true
	f.lastIsNew = true
	return true, nil
}

// UpsertImpression 模拟 Worker 补偿链路的聚合 upsert；clip 不可见时返回领域错误。
func (f *impressionFakeRepository) UpsertImpression(_ context.Context, event *domimpression.PlayEvent) error {
	f.upsertCalls++
	if f.upsertErr != nil {
		return f.upsertErr
	}
	if !f.clips[event.ClipID] {
		return domimpression.ErrClipNotFound
	}
	f.applyImpression(event)
	return nil
}

// MarkEventSynced 标记事件已完成曝光聚合。
func (f *impressionFakeRepository) MarkEventSynced(_ context.Context, eventID string) error {
	f.markCalls++
	if f.markErr != nil {
		return f.markErr
	}
	f.synced[eventID] = true
	return nil
}

// IsEventSynced 查询事件是否已完成曝光聚合。
func (f *impressionFakeRepository) IsEventSynced(_ context.Context, eventID string) (bool, error) {
	f.syncedCalls++
	if f.syncedErr != nil {
		return false, f.syncedErr
	}
	return f.synced[eventID], nil
}

// impressionFakePublisher 记录投递的 play.recorded 事件。
type impressionFakePublisher struct {
	events []appimpression.PlayRecorded
	err    error
}

// PublishPlayRecorded 记录事件并按需返回投递错误。
func (p *impressionFakePublisher) PublishPlayRecorded(_ context.Context, event appimpression.PlayRecorded) error {
	p.events = append(p.events, event)
	return p.err
}

// impressionFakeOnlyRow 断言聚合表恰好一行并返回该行。
func impressionFakeOnlyRow(t *testing.T, repo *impressionFakeRepository) *domimpression.Impression {
	t.Helper()
	if len(repo.impressions) != 1 {
		t.Fatalf("曝光聚合行数 = %d, 期望 1", len(repo.impressions))
	}
	for _, row := range repo.impressions {
		return row
	}
	return nil
}

// TestRecordPlayEventDedupByEventID 验证同一 event_id 重复上报只落一行、play_count 只计一次。
func TestRecordPlayEventDedupByEventID(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	service := appimpression.New(repo)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := service.RecordPlayEvent(ctx, playEventInput("evt-1", 10, 1200, "play", false)); err != nil {
			t.Fatalf("第 %d 次上报返回错误: %v", i+1, err)
		}
	}

	if len(repo.events) != 1 {
		t.Fatalf("播放事件行数 = %d, 期望 1", len(repo.events))
	}
	if repo.lastIsNew {
		t.Fatalf("重复 event_id 不应判定为新事件")
	}
	row := impressionFakeOnlyRow(t, repo)
	if row.PlayCount != 1 {
		t.Fatalf("play_count = %d, 期望 1", row.PlayCount)
	}
	if row.CompletedCount != 0 {
		t.Fatalf("completed_count = %d, 期望 0", row.CompletedCount)
	}
}

// TestRecordPlayEventAccumulatesDistinctEvents 验证不同 event_id 在同一 clip 上累加播放与完播计数。
func TestRecordPlayEventAccumulatesDistinctEvents(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	service := appimpression.New(repo)
	ctx := context.Background()

	if err := service.RecordPlayEvent(ctx, playEventInput("evt-1", 10, 800, "play", false)); err != nil {
		t.Fatalf("第一次上报返回错误: %v", err)
	}
	if err := service.RecordPlayEvent(ctx, playEventInput("evt-2", 10, 3000, "play", true)); err != nil {
		t.Fatalf("第二次上报返回错误: %v", err)
	}

	if len(repo.events) != 2 {
		t.Fatalf("播放事件行数 = %d, 期望 2", len(repo.events))
	}
	row := impressionFakeOnlyRow(t, repo)
	if row.PlayCount != 2 {
		t.Fatalf("play_count = %d, 期望 2", row.PlayCount)
	}
	if row.CompletedCount != 1 {
		t.Fatalf("completed_count = %d, 期望 1", row.CompletedCount)
	}
	if row.ViewerID != 7 || row.ClipID != 10 {
		t.Fatalf("聚合行归属 = (%d,%d), 期望 (7,10)", row.ViewerID, row.ClipID)
	}
}

// TestRecordPlayEventCountsOnlyPlay 验证计数口径：exposed/skip 只登记展示，
// 只有 play 累加 play_count，complete 只累加 completed_count。
func TestRecordPlayEventCountsOnlyPlay(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	service := appimpression.New(repo)
	ctx := context.Background()

	// 先上报展示：聚合行应被创建，但两个计数都为 0。
	if err := service.RecordPlayEvent(ctx, playEventInput("evt-exposed", 10, 0, "exposed", false)); err != nil {
		t.Fatalf("上报 exposed 返回错误: %v", err)
	}
	row := impressionFakeOnlyRow(t, repo)
	if row.PlayCount != 0 || row.CompletedCount != 0 {
		t.Fatalf("exposed 不应累加计数, 实际 play=%d complete=%d", row.PlayCount, row.CompletedCount)
	}

	// 播放、完播与划走：只有 play 动 play_count，complete 动 completed_count。
	for _, item := range []struct {
		eventID string
		event   string
		watchMs int64
	}{
		{eventID: "evt-play", event: "play", watchMs: 1200},
		{eventID: "evt-complete", event: "complete", watchMs: 5000},
		{eventID: "evt-skip", event: "skip", watchMs: 0},
	} {
		if err := service.RecordPlayEvent(ctx, playEventInput(item.eventID, 10, item.watchMs, item.event, item.event == "complete")); err != nil {
			t.Fatalf("上报 %s 返回错误: %v", item.event, err)
		}
	}

	if len(repo.events) != 4 {
		t.Fatalf("事件行数 = %d, 期望 4", len(repo.events))
	}
	row = impressionFakeOnlyRow(t, repo)
	if row.PlayCount != 1 {
		t.Fatalf("play_count = %d, 期望 1（仅 play 累加）", row.PlayCount)
	}
	if row.CompletedCount != 1 {
		t.Fatalf("completed_count = %d, 期望 1（仅 complete 累加）", row.CompletedCount)
	}

	// 同一 exposed event_id 重复上报不重复建行，也不改变计数。
	if err := service.RecordPlayEvent(ctx, playEventInput("evt-exposed", 10, 0, "exposed", false)); err != nil {
		t.Fatalf("重复上报 exposed 返回错误: %v", err)
	}
	if len(repo.events) != 4 {
		t.Fatalf("重复 exposed 后事件行数 = %d, 期望 4", len(repo.events))
	}
	if row.PlayCount != 1 || row.CompletedCount != 1 {
		t.Fatalf("重复 exposed 改变计数: play=%d complete=%d", row.PlayCount, row.CompletedCount)
	}
}

// TestRecordPlayEventDefaultsLegacyType 验证旧客户端未传 event_type 时按 play 语义计数。
func TestRecordPlayEventDefaultsLegacyType(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	service := appimpression.New(repo)

	if err := service.RecordPlayEvent(context.Background(), playEventInput("evt-legacy", 10, 1200, "", false)); err != nil {
		t.Fatalf("旧事件上报返回错误: %v", err)
	}
	if eventType := repo.events["evt-legacy"].EventType; eventType != domimpression.EventTypePlay {
		t.Fatalf("缺省事件类型 = %s, 期望 play", eventType)
	}
	row := impressionFakeOnlyRow(t, repo)
	if row.PlayCount != 1 {
		t.Fatalf("旧事件 play_count = %d, 期望 1", row.PlayCount)
	}
}

// TestRecordPlayEventRejectsInvalidInput 验证非法 event_id、clip_id 与时长在落库前被拒绝。
func TestRecordPlayEventRejectsInvalidInput(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	service := appimpression.New(repo)
	ctx := context.Background()

	cases := []struct {
		name     string
		clipID   int64
		eventID  string
		duration int64
		event    string
		scene    string
		request  string
		want     error
	}{
		{name: "空 event_id", clipID: 10, eventID: "", duration: 100, want: domimpression.ErrInvalidEventID},
		{name: "空白 event_id", clipID: 10, eventID: "   ", duration: 100, want: domimpression.ErrInvalidEventID},
		{name: "超长 event_id", clipID: 10, eventID: strings.Repeat("x", 129), duration: 100, want: domimpression.ErrInvalidEventID},
		{name: "clip_id 非正", clipID: 0, eventID: "evt-1", duration: 100, want: domimpression.ErrClipNotFound},
		{name: "时长为负", clipID: 10, eventID: "evt-1", duration: -1, want: domimpression.ErrInvalidPlayDuration},
		{name: "时长超单日上限", clipID: 10, eventID: "evt-1", duration: 24*60*60*1000 + 1, want: domimpression.ErrInvalidPlayDuration},
		{name: "未知事件类型", clipID: 10, eventID: "evt-1", duration: 100, event: "impression", want: domimpression.ErrInvalidEventType},
		{name: "未知场景", clipID: 10, eventID: "evt-1", duration: 100, event: "play", scene: "unknown", want: domimpression.ErrInvalidScene},
		{name: "超长 request_id", clipID: 10, eventID: "evt-1", duration: 100, event: "exposed", request: strings.Repeat("r", 129), want: domimpression.ErrInvalidRequestID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := playEventInput(tc.eventID, tc.clipID, tc.duration, tc.event, false)
			input.Scene = tc.scene
			input.RequestID = tc.request
			err := service.RecordPlayEvent(ctx, input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("错误 = %v, 期望 %v", err, tc.want)
			}
		})
	}
	if repo.recordCalls != 0 {
		t.Fatalf("非法入参不应调用仓储, record_calls=%d", repo.recordCalls)
	}
}

// TestRecordPlayEventIgnoresPublisherFailure 验证事件投递失败只记日志，不影响上报结果与落库。
func TestRecordPlayEventIgnoresPublisherFailure(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	publisher := &impressionFakePublisher{err: errors.New("消息队列不可用")}
	service := appimpression.New(repo, appimpression.WithPublisher(publisher))
	ctx := context.Background()

	if err := service.RecordPlayEvent(ctx, playEventInput("evt-1", 10, 900, "complete", true)); err != nil {
		t.Fatalf("投递失败不应影响上报, 实际返回 %v", err)
	}
	if len(repo.events) != 1 {
		t.Fatalf("播放事件行数 = %d, 期望 1", len(repo.events))
	}
	if len(publisher.events) != 1 {
		t.Fatalf("投递次数 = %d, 期望 1", len(publisher.events))
	}

	payload := publisher.events[0]
	if payload.EventID != "evt-1" || payload.ViewerID != 7 || payload.ClipID != 10 {
		t.Fatalf("投递载荷字段 = %+v, 期望 event_id=evt-1 viewer_id=7 clip_id=10", payload)
	}
	if payload.EventType != "complete" || payload.Scene != "latest" || payload.RequestID != "req-1" {
		t.Fatalf("投递载荷丢失事件类型/场景/批次: %+v", payload)
	}
	if payload.WatchMs != 900 {
		t.Fatalf("投递载荷 watch_ms = %d, 期望 900", payload.WatchMs)
	}
	if !payload.Completed || !payload.Impression {
		t.Fatalf("投递载荷丢失完成/展示标记: %+v", payload)
	}
	if payload.OccurredAt.IsZero() {
		t.Fatalf("投递载荷缺少发生时间")
	}
}

// TestPlayEventWorkerDeadLettersInvalidPayload 验证非法载荷标记为不可重试（转入死信），不查询同步状态也不补偿。
func TestPlayEventWorkerDeadLettersInvalidPayload(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	worker := appimpression.NewPlayEventWorker(repo)

	payloads := [][]byte{
		[]byte("not-json"),
		[]byte(`{"event_id":"","viewer_id":7,"clip_id":10}`),
		[]byte(`{"event_id":"evt-1","viewer_id":0,"clip_id":10}`),
		[]byte(`{"event_id":"evt-1","viewer_id":7,"clip_id":0}`),
	}
	for _, body := range payloads {
		if err := worker.Handle(context.Background(), body); !messaging.IsPermanent(err) {
			t.Fatalf("非法载荷 %s 应标记不可重试（转入死信）, 实际返回 %v", body, err)
		}
	}
	if repo.syncedCalls != 0 || repo.upsertCalls != 0 || repo.markCalls != 0 {
		t.Fatalf("非法载荷不应访问仓储, synced=%d upsert=%d mark=%d", repo.syncedCalls, repo.upsertCalls, repo.markCalls)
	}
}

// TestPlayEventWorkerConfirmsSyncedEvent 验证已同步事件重复消费直接确认，不产生二次聚合。
func TestPlayEventWorkerConfirmsSyncedEvent(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	repo.synced["evt-1"] = true

	body, err := json.Marshal(appimpression.PlayRecorded{
		EventID:        "evt-1",
		ViewerID:       7,
		ClipID:         10,
		WatchMs:        1200,
		Impression:     true,
		OccurredAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	worker := appimpression.NewPlayEventWorker(repo)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("已同步事件应直接确认, 实际返回 %v", err)
	}
	if repo.upsertCalls != 0 || repo.markCalls != 0 {
		t.Fatalf("已同步事件不应补偿, upsert=%d mark=%d", repo.upsertCalls, repo.markCalls)
	}
	if len(repo.impressions) != 0 {
		t.Fatalf("已同步事件不应新增聚合行, 实际 %d", len(repo.impressions))
	}
}

// TestPlayEventWorkerCompensatesUnsyncedEvent 验证未同步事件被补偿聚合并标记已同步，重复消费保持幂等。
func TestPlayEventWorkerCompensatesUnsyncedEvent(t *testing.T) {
	occurredAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	repo := newImpressionFakeRepository(10)
	body, err := json.Marshal(appimpression.PlayRecorded{
		EventID:        "evt-2",
		ViewerID:       7,
		ClipID:         10,
		EventType:      "complete",
		WatchMs:        1500,
		Completed:      true,
		Impression:     true,
		OccurredAt:     occurredAt,
	})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	worker := appimpression.NewPlayEventWorker(repo)
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("补偿消费返回错误: %v", err)
	}
	if repo.upsertCalls != 1 {
		t.Fatalf("补偿 upsert 次数 = %d, 期望 1", repo.upsertCalls)
	}
	if repo.markCalls != 1 {
		t.Fatalf("标记已同步次数 = %d, 期望 1", repo.markCalls)
	}
	if !repo.synced["evt-2"] {
		t.Fatalf("补偿后事件未标记为已同步")
	}

	row := impressionFakeOnlyRow(t, repo)
	if row.PlayCount != 0 || row.CompletedCount != 1 {
		t.Fatalf("补偿聚合 = play %d / complete %d, 期望 0 / 1（complete 不累加 play）", row.PlayCount, row.CompletedCount)
	}
	if row.Day != domimpression.Day(occurredAt) {
		t.Fatalf("聚合日 = %s, 期望 %s", row.Day, domimpression.Day(occurredAt))
	}

	// 已同步后重复消费：直接确认，不再二次累加。
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("重复消费返回错误: %v", err)
	}
	if repo.upsertCalls != 1 {
		t.Fatalf("重复消费不应再次补偿, upsert=%d", repo.upsertCalls)
	}
	if row.PlayCount != 0 || row.CompletedCount != 1 {
		t.Fatalf("重复消费后聚合被二次累加: play %d / complete %d", row.PlayCount, row.CompletedCount)
	}
}

// TestPlayEventWorkerDeadLettersMissingClip 验证补偿时 clip 不可见标记为不可重试（转入死信），避免毒消息无限回队。
func TestPlayEventWorkerDeadLettersMissingClip(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	body, err := json.Marshal(appimpression.PlayRecorded{
		EventID:        "evt-3",
		ViewerID:       7,
		ClipID:         99,
		WatchMs:        1000,
		OccurredAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	worker := appimpression.NewPlayEventWorker(repo)
	if err := worker.Handle(context.Background(), body); !messaging.IsPermanent(err) {
		t.Fatalf("clip 不可见应标记不可重试（转入死信）, 实际返回 %v", err)
	}
	if repo.markCalls != 0 {
		t.Fatalf("clip 不可见不应标记已同步, mark=%d", repo.markCalls)
	}
	if len(repo.impressions) != 0 {
		t.Fatalf("clip 不可见不应新增聚合行, 实际 %d", len(repo.impressions))
	}
}

// TestPlayEventWorkerReturnsErrorOnLookupFailure 验证查询同步状态失败时返回错误触发 MQ 重投。
func TestPlayEventWorkerReturnsErrorOnLookupFailure(t *testing.T) {
	repo := newImpressionFakeRepository(10)
	repo.syncedErr = errors.New("查询同步状态失败")
	body, err := json.Marshal(appimpression.PlayRecorded{
		EventID:    "evt-4",
		ViewerID:   7,
		ClipID:     10,
		OccurredAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	worker := appimpression.NewPlayEventWorker(repo)
	if err := worker.Handle(context.Background(), body); err == nil {
		t.Fatalf("查询同步状态失败应返回错误触发重投")
	}
	if repo.upsertCalls != 0 {
		t.Fatalf("查询失败不应继续补偿, upsert=%d", repo.upsertCalls)
	}
}
