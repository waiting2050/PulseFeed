// model.go 定义 relation 模块 pf_follows 与 pf_user_stats 两张表的 GORM 模型。
package infrarelation

import "time"

// FollowModel 映射 pf_follows 表；(user_id, target_user_id) 唯一索引保证同一对用户只有一行，
// 取关为状态置位，历史行保留用于审计；两条复合索引分别支撑关注/粉丝列表的游标分页。
type FollowModel struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	UserID         int64     `gorm:"column:user_id;not null;uniqueIndex:uk_user_target,priority:1;index:idx_user_status_updated,priority:1"`
	TargetUserID   int64     `gorm:"column:target_user_id;not null;uniqueIndex:uk_user_target,priority:2;index:idx_target_status_updated,priority:1;index:idx_user_status_updated,priority:4,sort:desc"`
	Status         int       `gorm:"column:status;not null;default:1;index:idx_user_status_updated,priority:2;index:idx_target_status_updated,priority:2"`
	IdempotencyKey string    `gorm:"column:idempotency_key;size:128"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime;index:idx_user_status_updated,priority:3,sort:desc;index:idx_target_status_updated,priority:3,sort:desc"`
}

// TableName 指定关注关系表名。
func (FollowModel) TableName() string {
	return "pf_follows"
}

// UserStatsModel 映射 pf_user_stats 表，保存关注数与粉丝数；与关系状态在同一事务内维护。
type UserStatsModel struct {
	UserID         int64     `gorm:"column:user_id;primaryKey"`
	FollowingCount int64     `gorm:"column:following_count;not null;default:0"`
	FollowerCount  int64     `gorm:"column:follower_count;not null;default:0"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定用户关系统计表名。
func (UserStatsModel) TableName() string {
	return "pf_user_stats"
}
