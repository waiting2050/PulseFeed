// model.go 定义 feed 模块查询 pf_clips 与 pf_clip_stats 联表结果的行映射；
// 两张表已在 clip 模块登记迁移，feed 不新增表（S11 起另直读 pf_follows/pf_user_stats）。
package infrafeed

import "time"

// latestRow 是 latest 场景分页查询的行映射：pf_clips 主表 LEFT JOIN pf_clip_stats 计数。
type latestRow struct {
	ID           int64     `gorm:"column:id"`
	CreatorID    int64     `gorm:"column:creator_id"`
	Title        string    `gorm:"column:title"`
	Description  string    `gorm:"column:description"`
	VideoURL     string    `gorm:"column:video_url"`
	CoverURL     string    `gorm:"column:cover_url"`
	DurationMS   int64     `gorm:"column:duration_ms"`
	PublishedAt  time.Time `gorm:"column:published_at"`
	LikeCount    int64     `gorm:"column:like_count"`
	SaveCount    int64     `gorm:"column:save_count"`
	CommentCount int64     `gorm:"column:comment_count"`
	PlayCount    int64     `gorm:"column:play_count"`
}

// cardRow 是卡片（clip 视图，不含计数）批量查询的行映射。
type cardRow struct {
	ID          int64     `gorm:"column:id"`
	CreatorID   int64     `gorm:"column:creator_id"`
	Title       string    `gorm:"column:title"`
	Description string    `gorm:"column:description"`
	VideoURL    string    `gorm:"column:video_url"`
	CoverURL    string    `gorm:"column:cover_url"`
	DurationMS  int64     `gorm:"column:duration_ms"`
	PublishedAt time.Time `gorm:"column:published_at"`
}

// statsRow 是互动计数批量查询的行映射。
type statsRow struct {
	ClipID       int64 `gorm:"column:clip_id"`
	LikeCount    int64 `gorm:"column:like_count"`
	SaveCount    int64 `gorm:"column:save_count"`
	CommentCount int64 `gorm:"column:comment_count"`
	PlayCount    int64 `gorm:"column:play_count"`
}

// followedCreatorRow 是读者关注创作者联查 pf_user_stats 的行映射。
type followedCreatorRow struct {
	CreatorID     int64 `gorm:"column:creator_id"`
	FollowerCount int64 `gorm:"column:follower_count"`
}

// hotRow 是 hot 场景 DB 回源的行映射：clip 视图 + 计数 + 计算出的热度分。
type hotRow struct {
	ID           int64     `gorm:"column:id"`
	CreatorID    int64     `gorm:"column:creator_id"`
	Title        string    `gorm:"column:title"`
	Description  string    `gorm:"column:description"`
	VideoURL     string    `gorm:"column:video_url"`
	CoverURL     string    `gorm:"column:cover_url"`
	DurationMS   int64     `gorm:"column:duration_ms"`
	PublishedAt  time.Time `gorm:"column:published_at"`
	LikeCount    int64     `gorm:"column:like_count"`
	SaveCount    int64     `gorm:"column:save_count"`
	CommentCount int64     `gorm:"column:comment_count"`
	PlayCount    int64     `gorm:"column:play_count"`
	HotScore     float64   `gorm:"column:hot_score"`
}
