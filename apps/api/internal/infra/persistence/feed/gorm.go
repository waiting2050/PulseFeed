// gorm.go 实现 feed 领域仓储端口：latest/following 场景复合游标分页、卡片与计数批量读取、
// 关注集与大 V 判定直读（S11 起 BatchGetEntries/BatchGetStats 由空实现转为真实查询）。
package infrafeed

import (
	"context"

	domclip "pulsefeed/internal/domain/clip"
	domfeed "pulsefeed/internal/domain/feed"
	dominteraction "pulsefeed/internal/domain/interaction"
	domrelation "pulsefeed/internal/domain/relation"

	"gorm.io/gorm"
)

// viewerActionRow 是 pf_actions 互动状态查询的行映射。
type viewerActionRow struct {
	ClipID     int64  `gorm:"column:clip_id"`
	ActionType string `gorm:"column:action_type"`
}

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

// BatchGetEntries 按 ID 批量读取卡片（clip 视图，不含计数）；
// 只返回可见 clip，已删除/不存在的 ID 缺席于结果集，由调用方跳过。
func (r *Repository) BatchGetEntries(ctx context.Context, clipIDs []int64) (map[int64]*domfeed.FeedEntry, error) {
	found := make(map[int64]*domfeed.FeedEntry, len(clipIDs))
	if len(clipIDs) == 0 {
		return found, nil
	}

	var rows []cardRow
	err := r.db.WithContext(ctx).
		Table("pf_clips").
		Select("id, creator_id, title, video_url, cover_url, published_at").
		Where("status = ? AND id IN ?", domclip.StatusPublished, clipIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		found[rows[i].ID] = &domfeed.FeedEntry{
			ClipID:      rows[i].ID,
			CreatorID:   rows[i].CreatorID,
			Title:       rows[i].Title,
			VideoURL:    rows[i].VideoURL,
			CoverURL:    rows[i].CoverURL,
			PublishedAt: rows[i].PublishedAt,
		}
	}
	return found, nil
}

// BatchGetStats 按 ID 批量读取互动计数；缺失计数行的 ID 缺席于结果集，调用方按 0 值处理。
func (r *Repository) BatchGetStats(ctx context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, error) {
	found := make(map[int64]*domfeed.EntryStats, len(clipIDs))
	if len(clipIDs) == 0 {
		return found, nil
	}

	var rows []statsRow
	err := r.db.WithContext(ctx).
		Table("pf_clip_stats").
		Select("clip_id, like_count, save_count, comment_count, play_count").
		Where("clip_id IN ?", clipIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		found[rows[i].ClipID] = &domfeed.EntryStats{
			LikeCount:    rows[i].LikeCount,
			SaveCount:    rows[i].SaveCount,
			CommentCount: rows[i].CommentCount,
			PlayCount:    rows[i].PlayCount,
		}
	}
	return found, nil
}

// ListFollowingPage 按 (published_at, id) 严格递减返回读者关注创作者的可见 clip 条目，
// 计数经 LEFT JOIN pf_clip_stats 一并读出；是 following 场景缓存不可用时的 DB 回源路径。
func (r *Repository) ListFollowingPage(ctx context.Context, viewerID int64, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	query := r.db.WithContext(ctx).
		Table("pf_clips AS c").
		Select("c.id, c.creator_id, c.title, c.video_url, c.cover_url, c.published_at, "+
			"s.like_count, s.save_count, s.comment_count, s.play_count").
		Joins("JOIN pf_follows f ON f.target_user_id = c.creator_id AND f.user_id = ? AND f.status = ?", viewerID, domrelation.StatusFollowing).
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

// ListFollowedCreators 返回读者关注中的创作者及其粉丝数（pf_user_stats 维护值，缺失按 0 计）。
func (r *Repository) ListFollowedCreators(ctx context.Context, viewerID int64) ([]*domfeed.FollowedCreator, error) {
	var rows []followedCreatorRow
	err := r.db.WithContext(ctx).
		Table("pf_follows AS f").
		Select("f.target_user_id AS creator_id, COALESCE(s.follower_count, 0) AS follower_count").
		Joins("LEFT JOIN pf_user_stats s ON s.user_id = f.target_user_id").
		Where("f.user_id = ? AND f.status = ?", viewerID, domrelation.StatusFollowing).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	creators := make([]*domfeed.FollowedCreator, 0, len(rows))
	for i := range rows {
		creators = append(creators, &domfeed.FollowedCreator{
			CreatorID:     rows[i].CreatorID,
			FollowerCount: rows[i].FollowerCount,
		})
	}
	return creators, nil
}

// CountFollowers 返回创作者的有效粉丝数（pf_user_stats 维护值）；缺失统计行按 0 计。
func (r *Repository) CountFollowers(ctx context.Context, creatorID int64) (int64, error) {
	var counts []int64
	err := r.db.WithContext(ctx).
		Table("pf_user_stats").
		Where("user_id = ?", creatorID).
		Pluck("follower_count", &counts).Error
	if err != nil {
		return 0, err
	}
	// 统计行不存在（老用户从未进入关系链路）视为 0 粉。
	if len(counts) == 0 {
		return 0, nil
	}
	return counts[0], nil
}

// ListFollowerIDs 返回创作者的全部有效粉丝 ID（pf_follows 有效关注行），供发布扇出按批推送。
func (r *Repository) ListFollowerIDs(ctx context.Context, creatorID int64) ([]int64, error) {
	var ids []int64
	err := r.db.WithContext(ctx).
		Table("pf_follows").
		Where("target_user_id = ? AND status = ?", creatorID, domrelation.StatusFollowing).
		Pluck("user_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ListCreatorRecentClips 返回创作者最近发布的 N 条可见 clip（含计数），按 (published_at, id) 递减。
func (r *Repository) ListCreatorRecentClips(ctx context.Context, creatorID int64, limit int) ([]*domfeed.FeedEntry, error) {
	var rows []latestRow
	err := r.db.WithContext(ctx).
		Table("pf_clips AS c").
		Select("c.id, c.creator_id, c.title, c.video_url, c.cover_url, c.published_at, "+
			"s.like_count, s.save_count, s.comment_count, s.play_count").
		Joins("LEFT JOIN pf_clip_stats s ON s.clip_id = c.id").
		Where("c.creator_id = ? AND c.status = ?", creatorID, domclip.StatusPublished).
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

// BatchGetViewerStates 批量读取读者对条目的互动状态：
// 只取 pf_actions 中有效（status=1）的行为，按类型分别置 liked/saved。
func (r *Repository) BatchGetViewerStates(ctx context.Context, viewerID int64, clipIDs []int64) (map[int64]*domfeed.ViewerState, error) {
	states := make(map[int64]*domfeed.ViewerState, len(clipIDs))
	if viewerID == 0 || len(clipIDs) == 0 {
		return states, nil
	}

	var rows []viewerActionRow
	err := r.db.WithContext(ctx).
		Table("pf_actions").
		Select("clip_id, action_type").
		Where("user_id = ? AND status = ? AND clip_id IN ?", viewerID, dominteraction.StatusActive, clipIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for i := range rows {
		state, ok := states[rows[i].ClipID]
		if !ok {
			state = &domfeed.ViewerState{}
			states[rows[i].ClipID] = state
		}
		switch dominteraction.ActionType(rows[i].ActionType) {
		case dominteraction.ActionLike:
			state.Liked = true
		case dominteraction.ActionSave:
			state.Saved = true
		}
	}
	return states, nil
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
