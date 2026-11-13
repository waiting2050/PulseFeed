// model.go 定义死信消息记录表模型。
package infradlq

import "time"

// MessageModel 是死信消息表 pf_dead_letter_messages 的 GORM 映射。
// dedupe_key 唯一：同一队列、同一 MessageId 与同一载荷重复消费时只落一行。
type MessageModel struct {
	ID          int64     `gorm:"column:id;primaryKey;autoIncrement"`
	DedupeKey   string    `gorm:"column:dedupe_key;size:512;not null;uniqueIndex:uk_dead_letter_dedupe"`
	SourceQueue string    `gorm:"column:source_queue;size:128;not null"`
	EventType   string    `gorm:"column:event_type;size:64;not null"`
	EventID     string    `gorm:"column:event_id;size:128;not null;default:''"`
	Reason      string    `gorm:"column:reason;size:32;not null"`
	Attempts    int       `gorm:"column:attempts;not null;default:0"`
	LastError   string    `gorm:"column:last_error;size:512;not null;default:''"`
	Payload     []byte    `gorm:"column:payload;type:text"`
	ReceivedAt  time.Time `gorm:"column:received_at;type:datetime(3);not null;index:idx_dead_letter_received"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 指定死信消息表名。
func (MessageModel) TableName() string {
	return "pf_dead_letter_messages"
}
