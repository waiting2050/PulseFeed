// model.go 定义 feed 模块查询 pf_clips 与 pf_clip_stats 联表结果的行映射；
// 两张表已在 clip 模块登记迁移，feed 不新增表。
package infrafeed

import "time"

// latestRow 是 latest 场景分页查询的行映射：pf_clips 主表 LEFT JOIN pf_clip_stats 计数。
type latestRow struct {
	ID           int64     `gorm:"column:id"`
	CreatorID    int64     `gorm:"column:creator_id"`
	Title        string    `gorm:"column:title"`
	VideoURL     string    `gorm:"column:video_url"`
	CoverURL     string    `gorm:"column:cover_url"`
	PublishedAt  time.Time `gorm:"column:published_at"`
	LikeCount    int64     `gorm:"column:like_count"`
	SaveCount    int64     `gorm:"column:save_count"`
	CommentCount int64     `gorm:"column:comment_count"`
	PlayCount    int64     `gorm:"column:play_count"`
}
