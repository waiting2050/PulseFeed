// gorm.go 实现 feed 领域仓储端口：latest 场景复合游标分页，批量读取接口待 S9 补齐。
package infrafeed

import (
	"context"

	domclip "pulsefeed/internal/domain/clip"
	domfeed "pulsefeed/internal/domain/feed"

	"gorm.io/gorm"
)

// Repository 是 feed 仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domfeed.Repository = (*Repository)(nil)

// New 创建 feed 仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// ListLatestPage 按 (published_at, id) 严格递减分页返回可见 clip 条目，
// 计数经 LEFT JOIN pf_clip_stats 一并读出；复合游标保证翻页不重不漏。
func (r *Repository) ListLatestPage(ctx context.Context, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	query := r.db.WithContext(ctx).
		Table("pf_clips AS c").
		Select("c.id, c.creator_id, c.title, c.video_url, c.cover_url, c.published_at, "+
			"s.like_count, s.save_count, s.comment_count, s.play_count").
		Joins("LEFT JOIN pf_clip_stats s ON s.clip_id = c.id").
		Where("c.status = ?", domclip.StatusPublished)
	if cursor != nil {
		// 复合游标：严格小于上一页最后一条的 (published_at, id)。
		query = query.Where(
			"c.published_at < ? OR (c.published_at = ? AND c.id < ?)",
			cursor.PublishedAt, cursor.PublishedAt, cursor.ClipID,
		)
	}

	var rows []latestRow
	err := query.
		Order("c.published_at DESC, c.id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	entries := make([]*domfeed.FeedEntry, 0, len(rows))
	for i := range rows {
		entries = append(entries, toEntry(&rows[i]))
	}
	return entries, nil
}

// BatchGetEntries 按 ID 批量读取条目；S8 卡片缓存回源时补齐，当前返回空集。
func (r *Repository) BatchGetEntries(_ context.Context, _ []int64) (map[int64]*domfeed.FeedEntry, error) {
	return map[int64]*domfeed.FeedEntry{}, nil
}

// BatchGetStats 按 ID 批量读取互动计数；S9 互动链路补齐，当前返回空集。
func (r *Repository) BatchGetStats(_ context.Context, _ []int64) (map[int64]*domfeed.EntryStats, error) {
	return map[int64]*domfeed.EntryStats{}, nil
}

// BatchGetViewerStates 批量读取读者互动状态；S9 互动链路补齐，当前返回空集（全部视为未互动）。
func (r *Repository) BatchGetViewerStates(_ context.Context, _ int64, _ []int64) (map[int64]*domfeed.ViewerState, error) {
	return map[int64]*domfeed.ViewerState{}, nil
}

// toEntry 将联表行转换为领域条目，读者状态由应用层组装阶段填充。
func toEntry(row *latestRow) *domfeed.FeedEntry {
	return &domfeed.FeedEntry{
		ClipID:      row.ID,
		CreatorID:   row.CreatorID,
		Title:       row.Title,
		VideoURL:    row.VideoURL,
		CoverURL:    row.CoverURL,
		PublishedAt: row.PublishedAt,
		Stats: domfeed.EntryStats{
			LikeCount:    row.LikeCount,
			SaveCount:    row.SaveCount,
			CommentCount: row.CommentCount,
			PlayCount:    row.PlayCount,
		},
	}
}
