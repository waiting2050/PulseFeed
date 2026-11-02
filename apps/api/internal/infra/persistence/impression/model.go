// model.go 定义曝光模块 pf_play_events 与 pf_impressions 两张表的 GORM 模型。
package infraimpression

import "time"

// PlayEventModel 映射 pf_play_events 表；event_id 唯一索引保证同一事件只落一行。
// synced 标记该事件的曝光聚合是否已完成：API 同步落库后置 true，Worker 消费时据此跳过重复聚合。
type PlayEventModel struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	EventID        string    `gorm:"column:event_id;size:128;not null;uniqueIndex:uk_event_id"`
	ViewerID       int64     `gorm:"column:viewer_id;not null;index:idx_viewer_clip,priority:1"`
	ClipID         int64     `gorm:"column:clip_id;not null;index:idx_viewer_clip,priority:2"`
	PlayDurationMs int64     `gorm:"column:play_duration_ms;not null;default:0"`
	Completed      bool      `gorm:"column:completed;not null;default:false"`
	Impression     bool      `gorm:"column:impression;not null;default:false"`
	Synced         bool      `gorm:"column:synced;not null;default:false"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime;index:idx_created_at"`
}

// TableName 指定播放事件表名。
func (PlayEventModel) TableName() string {
	return "pf_play_events"
}

// ImpressionModel 映射 pf_impressions 表；(viewer_id, clip_id, day) 唯一键支撑按日 upsert 聚合。
type ImpressionModel struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ViewerID       int64     `gorm:"column:viewer_id;not null;uniqueIndex:uk_viewer_clip_day,priority:1"`
	ClipID         int64     `gorm:"column:clip_id;not null;uniqueIndex:uk_viewer_clip_day,priority:2;index:idx_clip_day,priority:1"`
	Day            string    `gorm:"column:day;type:date;not null;uniqueIndex:uk_viewer_clip_day,priority:3;index:idx_clip_day,priority:2"`
	PlayCount      int64     `gorm:"column:play_count;not null;default:0"`
	CompletedCount int64     `gorm:"column:completed_count;not null;default:0"`
	FirstPlayedAt  time.Time `gorm:"column:first_played_at;not null"`
	LastPlayedAt   time.Time `gorm:"column:last_played_at;not null"`
}

// TableName 指定曝光聚合表名。
func (ImpressionModel) TableName() string {
	return "pf_impressions"
}
