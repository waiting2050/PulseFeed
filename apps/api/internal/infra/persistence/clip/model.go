// model.go 定义 clip 模块 pf_clips 与 pf_clip_stats 两张表的 GORM 模型。
package infraclip

import "time"

// ClipModel 映射 pf_clips 表；(creator_id, idempotency_key) 唯一索引支撑幂等发布。
type ClipModel struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	CreatorID      int64     `gorm:"column:creator_id;not null;uniqueIndex:uk_creator_idempotency,priority:1;index:idx_creator_published,priority:1"`
	Title          string    `gorm:"column:title;size:128;not null"`
	VideoURL       string    `gorm:"column:video_url;size:512;not null"`
	CoverURL       string    `gorm:"column:cover_url;size:512"`
	Status         int       `gorm:"column:status;not null;default:1"`
	IdempotencyKey string    `gorm:"column:idempotency_key;size:64;not null;uniqueIndex:uk_creator_idempotency,priority:2"`
	PublishedAt    time.Time `gorm:"column:published_at;not null;index:idx_creator_published,priority:2,sort:desc"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定 clip 表名。
func (ClipModel) TableName() string {
	return "pf_clips"
}

// ClipStatsModel 映射 pf_clip_stats 表，主键即 clip ID，计数行随发布同事务创建为 0 值。
type ClipStatsModel struct {
	ClipID       int64     `gorm:"column:clip_id;primaryKey"`
	LikeCount    int64     `gorm:"column:like_count;not null;default:0"`
	SaveCount    int64     `gorm:"column:save_count;not null;default:0"`
	CommentCount int64     `gorm:"column:comment_count;not null;default:0"`
	PlayCount    int64     `gorm:"column:play_count;not null;default:0"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定计数表名。
func (ClipStatsModel) TableName() string {
	return "pf_clip_stats"
}
