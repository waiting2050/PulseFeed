// gorm.go 实现 feed 领域仓储端口：latest/following/hot 场景分页、卡片与计数批量读取、
// 关注集与大 V 判定直读（S15 起 hot 场景按计算出的热度分回源排序）。
package infrafeed

import (
	"context"
	"fmt"
	"time"

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

// creatorProfileRow 是 pf_user_profiles 作者摘要查询的行映射。
type creatorProfileRow struct {
	UserID    int64  `gorm:"column:user_id"`
	Nickname  string `gorm:"column:nickname"`
	AvatarURL string `gorm:"column:avatar_url"`
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

// EnsureTimelineIndex 幂等创建 latest/following 回源查询所需的 timeline 索引：
// 先查 information_schema.statistics，已存在则跳过，否则按 (status, published_at DESC, id DESC) 建索引。
// published_at 在 pf_clips 上为 NOT NULL，无需额外的空值过滤列。
func EnsureTimelineIndex(db *gorm.DB) error {
	var count int64
	err := db.Raw(
		"SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		"pf_clips",
		"idx_clip_timeline",
	).Scan(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Exec("CREATE INDEX idx_clip_timeline ON pf_clips (status, published_at DESC, id DESC)").Error
}

// ListLatestPage 按 (published_at, id) 严格递减分页返回可见 clip 条目，
// 计数经 LEFT JOIN pf_clip_stats 一并读出；复合游标保证翻页不重不漏。
func (r *Repository) ListLatestPage(ctx context.Context, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	query := r.db.WithContext(ctx).
		Table("pf_clips AS c").
		Select("c.id, c.creator_id, c.title, c.description, c.video_url, c.cover_url, c.duration_ms, c.published_at, "+
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

// ListHotPage 按热度分严格递减分页返回可见 clip 条目，是 hot 场景 Redis 不可用或窗口为空时的回源路径。
// 热度分在 SQL 中按 ScoreHot 同一公式计算（互动权重 + 小时年龄幂衰减），不在表内新增预计算列；
// 复合游标为 (hot_score, id) 严格小于上一页最后一条。
func (r *Repository) ListHotPage(ctx context.Context, cursor *domfeed.Cursor, limit int) ([]*domfeed.FeedEntry, error) {
	expr := hotScoreExpression()
	now := time.Now()

	query := r.db.WithContext(ctx).
		Table("pf_clips AS c").
		Select("c.id, c.creator_id, c.title, c.description, c.video_url, c.cover_url, c.duration_ms, c.published_at, "+
			"s.like_count, s.save_count, s.comment_count, s.play_count, "+
			expr+" AS hot_score", now).
		Joins("LEFT JOIN pf_clip_stats s ON s.clip_id = c.id").
		Where("c.status = ?", domclip.StatusPublished)
	if cursor != nil {
		// 复合游标：严格小于上一页最后一条的 (hot_score, id)。
		query = query.Where(
			expr+" < ? OR ("+expr+" = ? AND c.id < ?)",
			now, cursor.Score, now, cursor.Score, cursor.ClipID,
		)
	}

	var rows []hotRow
	err := query.
		Order("hot_score DESC, c.id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	entries := make([]*domfeed.FeedEntry, 0, len(rows))
	for i := range rows {
		entry := &domfeed.FeedEntry{
			ClipID:      rows[i].ID,
			CreatorID:   rows[i].CreatorID,
			Title:       rows[i].Title,
			Description: rows[i].Description,
			VideoURL:    rows[i].VideoURL,
			CoverURL:    rows[i].CoverURL,
			DurationMS:  rows[i].DurationMS,
			PublishedAt: rows[i].PublishedAt,
			Stats: domfeed.EntryStats{
				LikeCount:    rows[i].LikeCount,
				SaveCount:    rows[i].SaveCount,
				CommentCount: rows[i].CommentCount,
				PlayCount:    rows[i].PlayCount,
			},
			Score: rows[i].HotScore,
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// hotScoreExpression 构造与 domfeed.ScoreHot 等价的 SQL 热度表达式：
// （点赞*3 + 收藏*4 + 评论*5 + 播放*1）/ (小时年龄 + 2)^1.5，未来发布时间按 0 年龄截断；
// 表达式内含一个 ? 占位符，由调用方绑定查询时刻。
func hotScoreExpression() string {
	return fmt.Sprintf(
		"((COALESCE(s.like_count, 0) * %d + COALESCE(s.save_count, 0) * %d + "+
			"COALESCE(s.comment_count, 0) * %d + COALESCE(s.play_count, 0) * %d) / "+
			"POW(GREATEST(TIMESTAMPDIFF(SECOND, c.published_at, ?), 0) / 3600.0 + 2, 1.5))",
		domfeed.HotWeightLike, domfeed.HotWeightSave, domfeed.HotWeightComment, domfeed.HotWeightPlay,
	)
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
		Select("id, creator_id, title, description, video_url, cover_url, duration_ms, published_at").
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
			Description: rows[i].Description,
			VideoURL:    rows[i].VideoURL,
			CoverURL:    rows[i].CoverURL,
			DurationMS:  rows[i].DurationMS,
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
		Select("c.id, c.creator_id, c.title, c.description, c.video_url, c.cover_url, c.duration_ms, c.published_at, "+
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
		Select("c.id, c.creator_id, c.title, c.description, c.video_url, c.cover_url, c.duration_ms, c.published_at, "+
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

// ListLatestClipIDs 按 (published_at, id) 递减返回最近发布的 N 个可见 clip_id；
// 供 playback 预加载列表使用（S16 接入）。
func (r *Repository) ListLatestClipIDs(ctx context.Context, limit int) ([]int64, error) {
	var ids []int64
	err := r.db.WithContext(ctx).
		Table("pf_clips").
		Where("status = ?", domclip.StatusPublished).
		Order("published_at DESC, id DESC").
		Limit(limit).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
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

// BatchGetCreatorProfiles 批量读取作者昵称与头像；资料缺失的作者不出现在结果中，由调用方兜底。
func (r *Repository) BatchGetCreatorProfiles(ctx context.Context, creatorIDs []int64) (map[int64]*domfeed.CreatorProfile, error) {
	profiles := make(map[int64]*domfeed.CreatorProfile, len(creatorIDs))
	if len(creatorIDs) == 0 {
		return profiles, nil
	}

	var rows []creatorProfileRow
	err := r.db.WithContext(ctx).
		Table("pf_user_profiles").
		Select("user_id, nickname, avatar_url").
		Where("user_id IN ?", creatorIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		profiles[rows[i].UserID] = &domfeed.CreatorProfile{
			Nickname:  rows[i].Nickname,
			AvatarURL: rows[i].AvatarURL,
		}
	}
	return profiles, nil
}

// BatchGetFollowedCreatorIDs 批量判断读者是否已关注指定作者，未登录读者返回空集合。
func (r *Repository) BatchGetFollowedCreatorIDs(ctx context.Context, viewerID int64, creatorIDs []int64) (map[int64]bool, error) {
	followed := make(map[int64]bool, len(creatorIDs))
	if viewerID == 0 || len(creatorIDs) == 0 {
		return followed, nil
	}

	var ids []int64
	err := r.db.WithContext(ctx).
		Table("pf_follows").
		Where("user_id = ? AND status = ? AND target_user_id IN ?", viewerID, domrelation.StatusFollowing, creatorIDs).
		Pluck("target_user_id", &ids).Error
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		followed[id] = true
	}
	return followed, nil
}

// toEntry 将联表行转换为领域条目，读者状态由应用层组装阶段填充。
func toEntry(row *latestRow) *domfeed.FeedEntry {
	return &domfeed.FeedEntry{
		ClipID:      row.ID,
		CreatorID:   row.CreatorID,
		Title:       row.Title,
		Description: row.Description,
		VideoURL:    row.VideoURL,
		CoverURL:    row.CoverURL,
		DurationMS:  row.DurationMS,
		PublishedAt: row.PublishedAt,
		Stats: domfeed.EntryStats{
			LikeCount:    row.LikeCount,
			SaveCount:    row.SaveCount,
			CommentCount: row.CommentCount,
			PlayCount:    row.PlayCount,
		},
	}
}
