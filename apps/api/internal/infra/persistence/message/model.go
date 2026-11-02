// model.go 定义 message 模块 pf_messages 表的 GORM 模型。
package inframessage

import "time"

// MessageModel 映射 pf_messages 表；uk_event_id 保证重复事件不新增消息，
// (receiver_user_id, created_at, id) 支撑未读数与列表游标查询。
type MessageModel struct {
	ID         int64      `gorm:"column:id;primaryKey;autoIncrement"`
	ReceiverID int64      `gorm:"column:receiver_user_id;not null;index:idx_receiver_created,priority:1"`
	ActorID    int64      `gorm:"column:actor_user_id;not null"`
	Kind       string     `gorm:"column:kind;size:32;not null"`
	TargetID   int64      `gorm:"column:target_id;not null;default:0"`
	EventID    string     `gorm:"column:event_id;size:128;not null;uniqueIndex:uk_event_id"`
	ReadAt     *time.Time `gorm:"column:read_at"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime;index:idx_receiver_created,priority:2,sort:desc"`
}

// TableName 指定消息表名。
func (MessageModel) TableName() string {
	return "pf_messages"
}
