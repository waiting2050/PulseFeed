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
type ActionWorker struct {
	repo dominteraction.Repository
}

// NewActionWorker 创建异步互动落库 Worker。
func NewActionWorker(repo dominteraction.Repository) *ActionWorker {
	return &ActionWorker{repo: repo}
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

	if _, err := w.repo.SetAction(ctx, event.UserID, event.ClipID, event.ActionType, event.Active, event.IdempotencyKey); err != nil {
		if isTerminalActionError(err) {
			// clip 已删除等终态错误：重试无意义，标记不可重试并转入死信队列。
			log.Printf("warning: action_changed 因终态错误转入死信队列 (user_id=%d clip_id=%d): %v",
				event.UserID, event.ClipID, err)
			return messaging.Permanent(fmt.Errorf("处理 action_changed 命中终态错误 (user_id=%d clip_id=%d): %w",
				event.UserID, event.ClipID, err))
		}
		return fmt.Errorf("落库动作事件失败 (user_id=%d clip_id=%d): %w", event.UserID, event.ClipID, err)
	}
	return nil
}

// isTerminalActionError 判断错误是否属于无需重试的领域终态。
func isTerminalActionError(err error) bool {
	return errors.Is(err, dominteraction.ErrClipNotFound) ||
		errors.Is(err, dominteraction.ErrInvalidActionType)
}
