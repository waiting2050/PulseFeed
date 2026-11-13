// repository.go 实现 outbox 事件仓储：供 relay 读取待投递事件并回写投递结果。
package infraoutbox

import (
	"context"
	"time"

	appoutbox "pulsefeed/internal/application/outbox"

	"gorm.io/gorm"
)

// Repository 是 outbox 仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足 relay 的存储端口。
var _ appoutbox.Store = (*Repository)(nil)

// New 创建 outbox 仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// NewEventModel 构造待插入的 outbox 行，供 clip 事务在业务写入时复用。
func NewEventModel(eventID, topic string, payload []byte, nextAttemptAt time.Time) *EventModel {
	return &EventModel{
		EventID:       eventID,
		Topic:         topic,
		Payload:       payload,
		Status:        StatusPending,
		NextAttemptAt: nextAttemptAt,
	}
}

// ListPending 按写入顺序读取已到重试时间的待投递事件，保证同一业务的事件按序投递。
func (r *Repository) ListPending(ctx context.Context, now time.Time, limit int) ([]appoutbox.Event, error) {
	var models []EventModel
	err := r.db.WithContext(ctx).
		Where("status = ? AND next_attempt_at <= ?", StatusPending, now).
		Order("id ASC").
		Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	events := make([]appoutbox.Event, 0, len(models))
	for i := range models {
		events = append(events, appoutbox.Event{
			ID:       models[i].ID,
			EventID:  models[i].EventID,
			Topic:    models[i].Topic,
			Payload:  models[i].Payload,
			Attempts: models[i].Attempts,
		})
	}
	return events, nil
}

// MarkPublished 将事件标记为已确认投递；仅更新 pending 行，避免覆盖并发处理结果。
func (r *Repository) MarkPublished(ctx context.Context, id int64, publishedAt time.Time) error {
	return r.db.WithContext(ctx).
		Model(&EventModel{}).
		Where("id = ? AND status = ?", id, StatusPending).
		Updates(map[string]any{
			"status":       StatusPublished,
			"published_at": publishedAt,
			"last_error":   "",
		}).Error
}

// MarkFailed 记录一次投递失败：累计尝试次数并写入下次重试时间；
// exhausted 为 true 时状态转为 failed，交由人工排查，不再自动重试。
func (r *Repository) MarkFailed(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, reason string, exhausted bool) error {
	status := StatusPending
	if exhausted {
		status = StatusFailed
	}
	return r.db.WithContext(ctx).
		Model(&EventModel{}).
		Where("id = ? AND status = ?", id, StatusPending).
		Updates(map[string]any{
			"status":          status,
			"attempts":        attempts,
			"next_attempt_at": nextAttemptAt,
			"last_error":      truncateReason(reason, 255),
		}).Error
}

// truncateReason 按字符截断错误原因，避免超出 last_error 列宽。
func truncateReason(reason string, limit int) string {
	runes := []rune(reason)
	if len(runes) <= limit {
		return reason
	}
	return string(runes[:limit])
}
