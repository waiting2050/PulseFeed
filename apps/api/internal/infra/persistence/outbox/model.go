// model.go 定义事务性 outbox 事件表模型与状态常量。
package infraoutbox

import "time"

// outbox 事件状态：pending 待投递、published 已确认投递、failed 超过重试上限需人工处理。
const (
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusFailed    = "failed"
)

// EventModel 是 outbox 事件表 pf_outbox_events 的 GORM 映射。
// 事件与业务数据同事务写入，由 relay 轮询投递；event_id 唯一用于重复投递时的幂等识别。
type EventModel struct {
	ID            int64      `gorm:"column:id;primaryKey;autoIncrement"`
	EventID       string     `gorm:"column:event_id;size:64;not null;uniqueIndex:uk_outbox_event_id"`
	Topic         string     `gorm:"column:topic;size:64;not null"`
	Payload       []byte     `gorm:"column:payload;type:json;not null"`
	Status        string     `gorm:"column:status;size:16;not null;default:pending;index:idx_outbox_dispatch,priority:1"`
	Attempts      int        `gorm:"column:attempts;not null;default:0"`
	NextAttemptAt time.Time  `gorm:"column:next_attempt_at;type:datetime(3);not null;index:idx_outbox_dispatch,priority:2"`
	LastError     string     `gorm:"column:last_error;size:255;not null;default:''"`
	PublishedAt   *time.Time `gorm:"column:published_at;type:datetime(3)"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定 outbox 事件表名。
func (EventModel) TableName() string {
	return "pf_outbox_events"
}
