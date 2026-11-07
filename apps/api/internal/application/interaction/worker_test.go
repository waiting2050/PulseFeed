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
