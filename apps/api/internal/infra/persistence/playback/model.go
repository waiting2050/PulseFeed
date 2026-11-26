// model.go 定义 playback 模块 pf_playback_configs 与 pf_playback_qos_reports 两张表的 GORM 模型。
package infraplayback

import "time"

// ConfigModel 映射 pf_playback_configs 表；user_id 唯一索引保证每用户一行。
// buffer_ms 为后加的缓冲目标时长列，AutoMigrate 只加列不重建表，旧行取默认值 2000。
type ConfigModel struct {
	UserID        int64     `gorm:"column:user_id;primaryKey"`
	PreloadCount  int       `gorm:"column:preload_count;not null;default:8"`
	QoSSampleRate float64   `gorm:"column:qos_sample_rate;not null;default:0.1"`
	BufferMS      int       `gorm:"column:buffer_ms;not null;default:2000"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定播放配置表名。
func (ConfigModel) TableName() string {
	return "pf_playback_configs"
}

// QoSReportModel 映射 pf_playback_qos_reports 表；uk_event_id 保证幂等。
type QoSReportModel struct {
	ID         int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ReporterID int64     `gorm:"column:reporter_user_id;not null;index:idx_reporter_created,priority:1"`
	ClipID     int64     `gorm:"column:clip_id;not null;index:idx_clip"`
	EventID    string    `gorm:"column:event_id;size:128;not null;uniqueIndex:uk_event_id"`
	LatencyMS  int       `gorm:"column:latency_ms;not null;default:0"`
	StallCount int       `gorm:"column:stall_count;not null;default:0"`
	Completed  bool      `gorm:"column:completed;not null;default:false"`
	ClientInfo string    `gorm:"column:client_info;size:256"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime;index:idx_reporter_created,priority:2,sort:desc"`
}

// TableName 指定 QoS 上报表名。
func (QoSReportModel) TableName() string {
	return "pf_playback_qos_reports"
}
