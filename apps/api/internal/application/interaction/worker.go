// worker.go 实现 ActionWorker：消费 interaction.action_changed 事件并把互动状态落库。
package appaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	dominteraction "pulsefeed/internal/domain/interaction"
	"pulsefeed/internal/domain/messaging"
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
