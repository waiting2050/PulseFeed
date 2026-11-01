// model.go 定义互动模块 pf_actions 与 pf_comments 两张表的 GORM 模型。
package infraaction

import "time"

// ActionModel 映射 pf_actions 表；(user_id, clip_id, action_type) 唯一索引保证同一行为只有一行。
type ActionModel struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	UserID         int64     `gorm:"column:user_id;not null;uniqueIndex:uk_user_clip_type,priority:1;index:idx_user_type_status,priority:1"`
	ClipID         int64     `gorm:"column:clip_id;not null;uniqueIndex:uk_user_clip_type,priority:2;index:idx_clip_type_status,priority:1"`
	ActionType     string    `gorm:"column:action_type;size:16;not null;uniqueIndex:uk_user_clip_type,priority:3;index:idx_clip_type_status,priority:2"`
	Status         int       `gorm:"column:status;not null;default:1;index:idx_clip_type_status,priority:3;index:idx_user_type_status,priority:3"`
	IdempotencyKey string    `gorm:"column:idempotency_key;size:128"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定行为表名。
func (ActionModel) TableName() string {
	return "pf_actions"
}

// CommentModel 映射 pf_comments 表；删除为状态置位，幂等键在 (user_id, idempotency_key) 上唯一。
// IdempotencyKey 使用指针以允许 NULL：MySQL 唯一索引允许多行 NULL，空串则会导致误冲突。
type CommentModel struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ClipID         int64     `gorm:"column:clip_id;not null;index:idx_clip_status_created,priority:1"`
	UserID         int64     `gorm:"column:user_id;not null;uniqueIndex:uk_user_idempotency,priority:1;index:idx_user_created,priority:1"`
	Content        string    `gorm:"column:content;size:1000;not null"`
	Status         int       `gorm:"column:status;not null;default:1;index:idx_clip_status_created,priority:2"`
	IdempotencyKey *string   `gorm:"column:idempotency_key;size:128;uniqueIndex:uk_user_idempotency,priority:2"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime;index:idx_clip_status_created,priority:3,sort:desc;index:idx_user_created,priority:2,sort:desc"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定评论表名。
func (CommentModel) TableName() string {
	return "pf_comments"
}
