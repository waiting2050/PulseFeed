// worker.go 实现 ActionWorker：消费 interaction.action_changed 事件并把互动状态落库。
package appaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	dominteraction "pulsefeed/internal/domain/interaction"
	"pulsefeed/internal/domain/messaging"
)

const (
	// ActionBatchSize 是 action_changed 单批最多聚合的消息数，与消费预取上限一致。
	ActionBatchSize = 200
	// ActionBatchWait 是攒批的最长等待时间：不足一批时最多等待该时长再落库。
	ActionBatchWait = 50 * time.Millisecond
	// actionBatchPartitions 是批内并行的固定分区数：同一 clip 固定在同一个分区内串行，
	// 不同 clip 落到不同分区并行落库，从而在保序的前提下削掉同一计数行的串行争用。
	actionBatchPartitions = 8
)

// ActionWorker 是 action_changed 队列的消费者，按事件把点赞/收藏状态写入 DB。
// 热度分钟桶在 DB 落库成功后累加：异步链路的“提交”发生在 Worker，热度必须跟在提交之后。
type ActionWorker struct {
	repo      dominteraction.Repository
	hotScores HotScoreRecorder
	metrics   InteractionMetrics
}

// ActionWorkerOption 用于装配 Worker 的可选依赖。
type ActionWorkerOption func(*ActionWorker)

// WithActionHotScoreRecorder 注入热度分钟桶写入器，落库成功后按事件方向累加。
func WithActionHotScoreRecorder(recorder HotScoreRecorder) ActionWorkerOption {
	return func(w *ActionWorker) {
		w.hotScores = recorder
	}
}

// WithActionWorkerMetrics 注入 Worker 侧的副作用指标。
func WithActionWorkerMetrics(metrics InteractionMetrics) ActionWorkerOption {
	return func(w *ActionWorker) {
		w.metrics = metrics
	}
}

// NewActionWorker 创建异步互动落库 Worker。
func NewActionWorker(repo dominteraction.Repository, opts ...ActionWorkerOption) *ActionWorker {
	w := &ActionWorker{repo: repo}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Name 返回 Worker 名称，供启动日志与指标区分。
func (w *ActionWorker) Name() string {
	return "action_changed"
}

// Handle 解析事件并调用仓储；格式错误与领域终态错误标记为不可重试（转入死信），
// 其余错误交给 MQ 重新入队。
func (w *ActionWorker) Handle(ctx context.Context, body []byte) error {
	var event ActionChanged
	if err := json.Unmarshal(body, &event); err != nil {
		// 非法载荷重试也不会成功：标记不可重试，由 MQ 转入死信队列。
		log.Printf("warning: action_changed 载荷非法，转入死信队列: %v", err)
		return messaging.Permanent(fmt.Errorf("解析 action_changed 载荷失败: %w", err))
	}
	if event.UserID <= 0 || event.ClipID <= 0 || !event.ActionType.Valid() {
		log.Printf("warning: action_changed 字段非法，转入死信队列 (user_id=%d clip_id=%d action_type=%s)",
			event.UserID, event.ClipID, event.ActionType)
		return messaging.Permanent(fmt.Errorf("action_changed 字段非法 (user_id=%d clip_id=%d action_type=%s)",
			event.UserID, event.ClipID, event.ActionType))
	}

	result, err := w.repo.SetAction(ctx, event.UserID, event.ClipID, event.ActionType, event.Active, event.IdempotencyKey)
	if err != nil {
		if isTerminalActionError(err) {
			// clip 已删除等终态错误：重试无意义，标记不可重试并转入死信队列。
			log.Printf("warning: action_changed 因终态错误转入死信队列 (user_id=%d clip_id=%d): %v",
				event.UserID, event.ClipID, err)
			return messaging.Permanent(fmt.Errorf("处理 action_changed 命中终态错误 (user_id=%d clip_id=%d): %w",
				event.UserID, event.ClipID, err))
		}
		return fmt.Errorf("落库动作事件失败 (user_id=%d clip_id=%d): %w", event.UserID, event.ClipID, err)
	}

	// 热度属于非正确性写入，只在 DB 提交成功后累加；失败只记日志与指标并返回成功，
	// 避免 MQ 因热度故障重投（DB 状态已幂等，重投也无法补写热度）。
	if result != nil && result.Changed && w.hotScores != nil {
		if err := w.hotScores.RecordHotScore(ctx, event.ClipID, hotScoreDelta(event.ActionType, event.Active)); err != nil {
			log.Printf("warning: action_changed 落库后热度写入失败 (clip_id=%d): %v", event.ClipID, err)
			observeSideEffect(w.metrics, sideEffectHotScore, err)
		} else {
			observeSideEffect(w.metrics, sideEffectHotScore, nil)
		}
	}
	return nil
}

// isTerminalActionError 判断错误是否属于无需重试的领域终态。
func isTerminalActionError(err error) bool {
	return errors.Is(err, dominteraction.ErrClipNotFound) ||
		errors.Is(err, dominteraction.ErrInvalidActionType)
}

// actionBatchItem 是批内一条事件及其在原始投递中的下标，用于回填逐条结果。
type actionBatchItem struct {
	index int
	event ActionChanged
}

// HandleBatch 批量消费 action_changed：解析校验后按 clip 分组，同一 clip 的事件在固定分区内串行，
// 不同分区并行落库；返回的错误与入参顺序一一对应，供消费端逐条 ack/nack。
func (w *ActionWorker) HandleBatch(ctx context.Context, bodies [][]byte) []error {
	errs := make([]error, len(bodies))

	groups := make(map[int64][]actionBatchItem, len(bodies))
	for i, body := range bodies {
		var event ActionChanged
		if err := json.Unmarshal(body, &event); err != nil {
			// 非法载荷重试也不会成功：标记不可重试，由 MQ 转入死信队列。
			log.Printf("warning: action_changed 载荷非法，转入死信队列: %v", err)
			errs[i] = messaging.Permanent(fmt.Errorf("解析 action_changed 载荷失败: %w", err))
			continue
		}
		if event.UserID <= 0 || event.ClipID <= 0 || !event.ActionType.Valid() {
			log.Printf("warning: action_changed 字段非法，转入死信队列 (user_id=%d clip_id=%d action_type=%s)",
				event.UserID, event.ClipID, event.ActionType)
			errs[i] = messaging.Permanent(fmt.Errorf("action_changed 字段非法 (user_id=%d clip_id=%d action_type=%s)",
				event.UserID, event.ClipID, event.ActionType))
			continue
		}
		groups[event.ClipID] = append(groups[event.ClipID], actionBatchItem{index: i, event: event})
	}

	// 按 clip_id 哈希固定分区：同一 clip 的先后顺序由分区内的串行处理保证。
	shards := make([][]int64, actionBatchPartitions)
	for clipID := range groups {
		part := clipID % actionBatchPartitions
		shards[part] = append(shards[part], clipID)
	}

	var wg sync.WaitGroup
	for _, clipIDs := range shards {
		if len(clipIDs) == 0 {
			continue
		}
		wg.Add(1)
		go func(clipIDs []int64) {
			defer wg.Done()
			for _, clipID := range clipIDs {
				w.applyBatchGroup(ctx, clipID, groups[clipID], errs)
			}
		}(clipIDs)
	}
	wg.Wait()
	return errs
}

// applyBatchGroup 落库同一 clip 的一批事件，并把逐条结果写回 errs 对应下标。
func (w *ActionWorker) applyBatchGroup(ctx context.Context, clipID int64, items []actionBatchItem, errs []error) {
	changes := make([]dominteraction.ActionChange, len(items))
	for i, item := range items {
		changes[i] = dominteraction.ActionChange{
			UserID:         item.event.UserID,
			ActionType:     item.event.ActionType,
			Active:         item.event.Active,
			IdempotencyKey: item.event.IdempotencyKey,
		}
	}

	results, err := w.repo.ApplyBatch(ctx, clipID, changes)
	if err != nil {
		terminal := isTerminalActionError(err)
		if terminal {
			log.Printf("warning: action_changed 因终态错误转入死信队列 (clip_id=%d, count=%d): %v", clipID, len(items), err)
		}
		for _, item := range items {
			if terminal {
				errs[item.index] = messaging.Permanent(fmt.Errorf(
					"处理 action_changed 命中终态错误 (user_id=%d clip_id=%d): %w", item.event.UserID, clipID, err))
				continue
			}
			errs[item.index] = fmt.Errorf("落库动作事件失败 (user_id=%d clip_id=%d): %w", item.event.UserID, clipID, err)
		}
		return
	}

	// 同一批内同一 clip 的热度只写一次：按 Changed 事件汇总方向增量，避免放大 Redis 写次数。
	var hotDelta float64
	for i, result := range results {
		if result.Changed {
			hotDelta += hotScoreDelta(items[i].event.ActionType, items[i].event.Active)
		}
	}
	if hotDelta == 0 || w.hotScores == nil {
		return
	}
	if err := w.hotScores.RecordHotScore(ctx, clipID, hotDelta); err != nil {
		log.Printf("warning: action_changed 落库后热度写入失败 (clip_id=%d): %v", clipID, err)
		observeSideEffect(w.metrics, sideEffectHotScore, err)
		return
	}
	observeSideEffect(w.metrics, sideEffectHotScore, nil)
}
