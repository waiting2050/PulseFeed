// worker_test.go 用 fake 仓储验证 ActionWorker 对 action_changed 重复投递的幂等语义：
// 同一事件消费两次只累计一次计数；首次已落库但回报失败、或中间一次失败后重投，都不补成双份。
package appaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appaction "pulsefeed/internal/application/interaction"
	dominteraction "pulsefeed/internal/domain/interaction"
	"pulsefeed/internal/domain/messaging"
)

// errActionWorkerTransient 模拟 MQ 重投前遇到的瞬时落库失败（非领域终态）。
var errActionWorkerTransient = errors.New("模拟瞬时落库失败")

// actionWorkerFake 在内存互动仓储之上注入失败点与调用计数，用于验证 Worker 的重投语义。
type actionWorkerFake struct {
	*interactionFake
	calls              int
	failNext           bool
	failAfterApplyOnce bool
}

// SetAction 先按开关注入失败，再委托内存仓储维护状态与计数；
// failAfterApplyOnce 模拟“事务已落库但调用方收到失败回报”的边界。
func (f *actionWorkerFake) SetAction(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, error) {
	f.calls++
	if f.failNext {
		f.failNext = false
		return nil, errActionWorkerTransient
	}
	failAfterApply := f.failAfterApplyOnce
	f.failAfterApplyOnce = false

	result, err := f.interactionFake.SetAction(ctx, userID, clipID, actionType, active, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if failAfterApply {
		return nil, errActionWorkerTransient
	}
	return result, nil
}

// newActionWorkerFake 构造仅发布 clip 11 的可注入失败仓储。
func newActionWorkerFake() *actionWorkerFake {
	return &actionWorkerFake{interactionFake: newInteractionFake(11)}
}

// actionChangedBody 构造一条合法的 action_changed 事件载荷。
func actionChangedBody(t *testing.T, active bool) []byte {
	t.Helper()
	body, err := json.Marshal(appaction.ActionChanged{
		UserID:         7,
		ClipID:         11,
		ActionType:     dominteraction.ActionLike,
		Active:         active,
		IdempotencyKey: "evt-1",
		OccurredAt:     time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("序列化 action_changed 事件失败: %v", err)
	}
	return body
}

// actionLikeState 读取 fake 中记录的行为状态，便于断言是否只落一行。
func actionLikeState(repo *actionWorkerFake) (*dominteraction.Action, bool) {
	action, ok := repo.actions[interactionFakeActionKey{userID: 7, clipID: 11, actionType: dominteraction.ActionLike}]
	return action, ok
}

// TestActionWorkerDuplicateDeliveryCountsOnce 验证同一 action_changed 事件被消费两次时计数只变化一次。
func TestActionWorkerDuplicateDeliveryCountsOnce(t *testing.T) {
	repo := newActionWorkerFake()
	worker := appaction.NewActionWorker(repo)
	ctx := context.Background()
	body := actionChangedBody(t, true)

	for i := 0; i < 2; i++ {
		if err := worker.Handle(ctx, body); err != nil {
			t.Fatalf("第 %d 次消费同一事件失败: %v", i+1, err)
		}
	}

	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("同一事件消费两次后点赞计数 = %d, 期望 1（重复投递不得二次累加）", got)
	}
	if action, ok := actionLikeState(repo); !ok {
		t.Fatalf("消费两次后应只保留一条行为记录")
	} else if action.Status != dominteraction.StatusActive {
		t.Fatalf("行为状态 = %d, 期望 %d", action.Status, dominteraction.StatusActive)
	}
	if repo.calls != 2 {
		t.Fatalf("仓储 SetAction 调用 = %d 次, 期望 2（Worker 不自行去重，幂等由仓储语义保证）", repo.calls)
	}
}

// TestActionWorkerRedeliveryAfterCommittedFailureKeepsSingleCount 验证首次已落库但回报失败时，
// Worker 返回错误触发重投；重投命中既有状态不再累加计数。
func TestActionWorkerRedeliveryAfterCommittedFailureKeepsSingleCount(t *testing.T) {
	repo := newActionWorkerFake()
	repo.failAfterApplyOnce = true
	worker := appaction.NewActionWorker(repo)
	ctx := context.Background()
	body := actionChangedBody(t, true)

	err := worker.Handle(ctx, body)
	if !errors.Is(err, errActionWorkerTransient) {
		t.Fatalf("首次已落库但回报失败时应返回可重试错误, 实际 %v", err)
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("落库后回报失败时计数 = %d, 期望 1", got)
	}

	// MQ 重投：状态已是 Active，仓储幂等命中，不得再累加。
	if err := worker.Handle(ctx, body); err != nil {
		t.Fatalf("重投应成功确认, 实际 %v", err)
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("重投后点赞计数 = %d, 期望仍为 1（不得补成双份）", got)
	}
	if repo.calls != 2 {
		t.Fatalf("仓储 SetAction 调用 = %d 次, 期望 2", repo.calls)
	}
}

// TestActionWorkerRecordsHotScoreAfterCommit 验证热度分钟桶在落库成功后按方向累加，
// 重复投递（幂等命中 changed=false）不会重复加热度。
func TestActionWorkerRecordsHotScoreAfterCommit(t *testing.T) {
	repo := newActionWorkerFake()
	recorder := &interactionFakeHotRecorder{}
	metrics := &interactionFakeMetrics{}
	worker := appaction.NewActionWorker(repo,
		appaction.WithActionHotScoreRecorder(recorder),
		appaction.WithActionWorkerMetrics(metrics),
	)
	ctx := context.Background()

	if err := worker.Handle(ctx, actionChangedBody(t, true)); err != nil {
		t.Fatalf("首次消费失败: %v", err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].clipID != 11 || recorder.calls[0].delta != 3 {
		t.Fatalf("热度写入 = %+v, 期望一次 clip=11 delta=3", recorder.calls)
	}
	if !metrics.has("hot_score:ok") {
		t.Fatalf("热度成功应上报 hot_score:ok，实际 %v", metrics.sideEffects)
	}

	// 同一事件重投：状态未翻转，热度不得重复累加。
	if err := worker.Handle(ctx, actionChangedBody(t, true)); err != nil {
		t.Fatalf("重投消费失败: %v", err)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("重复投递不应重复写热度，实际 %d 次: %+v", len(recorder.calls), recorder.calls)
	}
}

// TestActionWorkerHotScoreFailureDoesNotFailHandling 验证热度写失败只记日志与指标，
// Worker 仍确认消息（DB 已提交，重投也无法补写热度）。
func TestActionWorkerHotScoreFailureDoesNotFailHandling(t *testing.T) {
	repo := newActionWorkerFake()
	recorder := &interactionFakeHotRecorder{err: errors.New("redis down")}
	metrics := &interactionFakeMetrics{}
	worker := appaction.NewActionWorker(repo,
		appaction.WithActionHotScoreRecorder(recorder),
		appaction.WithActionWorkerMetrics(metrics),
	)

	if err := worker.Handle(context.Background(), actionChangedBody(t, true)); err != nil {
		t.Fatalf("热度失败不应导致消息重投，实际错误: %v", err)
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("热度失败不影响落库，点赞计数应为 1，实际 %d", got)
	}
	if !metrics.has("hot_score:error") {
		t.Fatalf("热度失败应上报 hot_score:error，实际 %v", metrics.sideEffects)
	}
}

// actionChangedBodyFor 按指定用户、作品与幂等键构造 action_changed 事件载荷。
func actionChangedBodyFor(t *testing.T, userID, clipID int64, active bool, idempotencyKey string) []byte {
	t.Helper()
	body, err := json.Marshal(appaction.ActionChanged{
		UserID:         userID,
		ClipID:         clipID,
		ActionType:     dominteraction.ActionLike,
		Active:         active,
		IdempotencyKey: idempotencyKey,
		OccurredAt:     time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("序列化 action_changed 事件失败: %v", err)
	}
	return body
}

// TestActionWorkerBatchAggregatesHotScoreOnce 验证同一批同作品的多条事件只写一次热度，且增量按方向汇总。
func TestActionWorkerBatchAggregatesHotScoreOnce(t *testing.T) {
	repo := newInteractionFake(11)
	recorder := &interactionFakeHotRecorder{}
	worker := appaction.NewActionWorker(repo, appaction.WithActionHotScoreRecorder(recorder))

	errs := worker.HandleBatch(context.Background(), [][]byte{
		actionChangedBodyFor(t, 7, 11, true, "e7"),
		actionChangedBodyFor(t, 8, 11, true, "e8"),
		actionChangedBodyFor(t, 9, 11, true, "e9"),
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 条批量事件处理失败: %v", i, err)
		}
	}
	if got := repo.likeCounts[11]; got != 3 {
		t.Fatalf("批量落库后点赞计数 = %d, 期望 3", got)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].clipID != 11 || recorder.calls[0].delta != 9 {
		t.Fatalf("热度写入 = %+v, 期望一次 clip=11 delta=9", recorder.calls)
	}
}

// TestActionWorkerBatchNetZeroHotScoreSkipped 验证同批内先赞后取消时热度净增量为 0，不写 Redis。
func TestActionWorkerBatchNetZeroHotScoreSkipped(t *testing.T) {
	repo := newInteractionFake(11)
	recorder := &interactionFakeHotRecorder{}
	worker := appaction.NewActionWorker(repo, appaction.WithActionHotScoreRecorder(recorder))

	errs := worker.HandleBatch(context.Background(), [][]byte{
		actionChangedBodyFor(t, 7, 11, true, "e1"),
		actionChangedBodyFor(t, 7, 11, false, "e2"),
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 条批量事件处理失败: %v", i, err)
		}
	}
	if got := repo.likeCounts[11]; got != 0 {
		t.Fatalf("先赞后取消后计数 = %d, 期望 0", got)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("净热度为 0 不应写 Redis，实际 %+v", recorder.calls)
	}
}

// TestActionWorkerBatchDuplicateDeliveryCountsOnce 验证同批重复投递与跨批重投都不重复累加。
func TestActionWorkerBatchDuplicateDeliveryCountsOnce(t *testing.T) {
	repo := newInteractionFake(11)
	worker := appaction.NewActionWorker(repo)
	body := actionChangedBodyFor(t, 7, 11, true, "evt-1")

	for _, batch := range [][][]byte{{body, body}, {body}} {
		errs := worker.HandleBatch(context.Background(), batch)
		for _, err := range errs {
			if err != nil {
				t.Fatalf("批量事件处理失败: %v", err)
			}
		}
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("重复投递后点赞计数 = %d, 期望 1", got)
	}
}

// TestActionWorkerBatchKeepsPerClipOrder 验证同作品事件按投递顺序重放，状态与计数与逐条写一致。
func TestActionWorkerBatchKeepsPerClipOrder(t *testing.T) {
	repo := newInteractionFake(11)
	worker := appaction.NewActionWorker(repo)

	errs := worker.HandleBatch(context.Background(), [][]byte{
		actionChangedBodyFor(t, 7, 11, true, "e1"),
		actionChangedBodyFor(t, 7, 11, false, "e2"),
		actionChangedBodyFor(t, 7, 11, true, "e3"),
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 条批量事件处理失败: %v", i, err)
		}
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("赞-取消-赞后点赞计数 = %d, 期望 1", got)
	}
	action, ok := repo.actions[interactionFakeActionKey{userID: 7, clipID: 11, actionType: dominteraction.ActionLike}]
	if !ok || action.Status != dominteraction.StatusActive {
		t.Fatalf("最终行为状态 = %+v, 期望 Active", action)
	}
}

// TestActionWorkerBatchGroupsByClip 验证批内不同作品各自落库，计数互不影响。
func TestActionWorkerBatchGroupsByClip(t *testing.T) {
	repo := newInteractionFake(11, 12, 13)
	worker := appaction.NewActionWorker(repo)

	errs := worker.HandleBatch(context.Background(), [][]byte{
		actionChangedBodyFor(t, 7, 11, true, "a"),
		actionChangedBodyFor(t, 7, 12, true, "b"),
		actionChangedBodyFor(t, 8, 12, true, "c"),
		actionChangedBodyFor(t, 7, 13, true, "d"),
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 条批量事件处理失败: %v", i, err)
		}
	}
	if repo.likeCounts[11] != 1 || repo.likeCounts[12] != 2 || repo.likeCounts[13] != 1 {
		t.Fatalf("各作品计数 = 11:%d 12:%d 13:%d, 期望 1/2/1",
			repo.likeCounts[11], repo.likeCounts[12], repo.likeCounts[13])
	}
}

// TestActionWorkerBatchTerminalError 验证作品不存在时整组消息都标记为不可重试。
func TestActionWorkerBatchTerminalError(t *testing.T) {
	repo := newInteractionFake(11)
	worker := appaction.NewActionWorker(repo)

	errs := worker.HandleBatch(context.Background(), [][]byte{
		actionChangedBodyFor(t, 7, 999, true, "a"),
		actionChangedBodyFor(t, 7, 11, true, "b"),
	})
	if len(errs) != 2 {
		t.Fatalf("错误切片长度 = %d, 期望 2", len(errs))
	}
	if errs[0] == nil || !messaging.IsPermanent(errs[0]) {
		t.Fatalf("作品不存在的错误应标记不可重试，实际 %v", errs[0])
	}
	if errs[1] != nil {
		t.Fatalf("同批其他作品的正常事件不应受影响，实际 %v", errs[1])
	}
}

// TestActionWorkerBatchInvalidPayload 验证非法载荷只影响自身，合法事件照常落库。
func TestActionWorkerBatchInvalidPayload(t *testing.T) {
	repo := newInteractionFake(11)
	worker := appaction.NewActionWorker(repo)

	errs := worker.HandleBatch(context.Background(), [][]byte{
		[]byte("not-json"),
		actionChangedBodyFor(t, 7, 11, true, "ok"),
	})
	if errs[0] == nil || !messaging.IsPermanent(errs[0]) {
		t.Fatalf("非法载荷应标记不可重试，实际 %v", errs[0])
	}
	if errs[1] != nil {
		t.Fatalf("合法事件不应受影响，实际 %v", errs[1])
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("合法事件应正常落库，点赞计数 = %d, 期望 1", got)
	}
}

// TestActionWorkerTransientFailureThenRedeliveryCountsOnce 验证首次成功后中间一次瞬时失败触发重投，
// 再投成功时计数仍为 1。
func TestActionWorkerTransientFailureThenRedeliveryCountsOnce(t *testing.T) {
	repo := newActionWorkerFake()
	worker := appaction.NewActionWorker(repo)
	ctx := context.Background()
	body := actionChangedBody(t, true)

	if err := worker.Handle(ctx, body); err != nil {
		t.Fatalf("首次消费失败: %v", err)
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("首次消费后点赞计数 = %d, 期望 1", got)
	}

	repo.failNext = true
	if err := worker.Handle(ctx, body); !errors.Is(err, errActionWorkerTransient) {
		t.Fatalf("瞬时失败应返回可重试错误, 实际 %v", err)
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("失败的一轮不应改变计数, 实际 %d", got)
	}

	if err := worker.Handle(ctx, body); err != nil {
		t.Fatalf("重投应成功确认, 实际 %v", err)
	}
	if got := repo.likeCounts[11]; got != 1 {
		t.Fatalf("重投后点赞计数 = %d, 期望仍为 1", got)
	}
	if repo.calls != 3 {
		t.Fatalf("仓储 SetAction 调用 = %d 次, 期望 3", repo.calls)
	}
}
