// worker.go 实现 ActionWorker：消费 interaction.action_changed 事件并把互动状态落库。
package appaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	dominteraction "pulsefeed/internal/domain/interaction"
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

// Handle 解析事件并调用仓储；格式错误与领域终态错误直接确认消息，其余错误交给 MQ 重新入队。
func (w *ActionWorker) Handle(ctx context.Context, body []byte) error {
	var event ActionChanged
	if err := json.Unmarshal(body, &event); err != nil {
		// 非法载荷重试也不会成功，记录后按已消费处理，避免毒消息无限回队。
		log.Printf("warning: 丢弃非法 action_changed 载荷: %v", err)
		return nil
	}
	if event.UserID <= 0 || event.ClipID <= 0 || !event.ActionType.Valid() {
		log.Printf("warning: 丢弃字段非法的 action_changed 载荷 (user_id=%d clip_id=%d action_type=%s)",
			event.UserID, event.ClipID, event.ActionType)
		return nil
	}

	if _, err := w.repo.SetAction(ctx, event.UserID, event.ClipID, event.ActionType, event.Active, event.IdempotencyKey); err != nil {
		if isTerminalActionError(err) {
			// clip 已删除等终态错误：重试无意义，记录后确认消息。
			log.Printf("warning: action_changed 因终态错误被丢弃 (user_id=%d clip_id=%d): %v",
				event.UserID, event.ClipID, err)
			return nil
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
