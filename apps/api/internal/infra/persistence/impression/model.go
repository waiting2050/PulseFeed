// model.go 定义展示模块 pf_play_events 与 pf_impressions 两张表的 GORM 模型。
package infraimpression

import "time"

// PlayEventModel 映射 pf_play_events 表；event_id 唯一索引保证同一事件只落一行。
// event_type 缺省为 play，保证迁移前写入的旧行仍按播放事件语义读取。
// play_duration_ms 为旧列保留兼容写入，watch_ms 为当前观看时长字段。
// synced 标记该事件的展示聚合是否已完成：API 同步落库后置 true，Worker 消费时据此跳过重复聚合。
type PlayEventModel struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	EventID        string    `gorm:"column:event_id;size:128;not null;uniqueIndex:uk_event_id"`
	ViewerID       int64     `gorm:"column:viewer_id;not null;index:idx_viewer_clip,priority:1"`
	ClipID         int64     `gorm:"column:clip_id;not null;index:idx_viewer_clip,priority:2"`
	EventType      string    `gorm:"column:event_type;size:16;not null;default:play"`
	Scene          string    `gorm:"column:scene;size:32;not null;default:'';index:idx_scene_request,priority:1"`
	RequestID      string    `gorm:"column:request_id;size:128;not null;default:'';index:idx_scene_request,priority:2"`
	WatchMs        int64     `gorm:"column:watch_ms;not null;default:0"`
	PlayDurationMs int64     `gorm:"column:play_duration_ms;not null;default:0"`
	Completed      bool      `gorm:"column:completed;not null;default:false"`
	Impression     bool      `gorm:"column:impression;not null;default:false"`
	Synced         bool      `gorm:"column:synced;not null;default:false"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime;index:idx_created_at"`
}

// TableName 指定展示事件表名。
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

// TableName 指定展示聚合表名。
func (ImpressionModel) TableName() string {
	return "pf_impressions"
}
