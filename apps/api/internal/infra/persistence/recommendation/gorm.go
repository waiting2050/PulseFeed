// gorm.go 实现 recommendation 领域仓储：按快照时间召回的候选池、兴趣行为聚合与曝光查询。
package infrarecommendation

import (
	"context"
	"encoding/json"
	"time"

	domclip "pulsefeed/internal/domain/clip"
	domembedding "pulsefeed/internal/domain/embedding"
	domrecommendation "pulsefeed/internal/domain/recommendation"

	"gorm.io/gorm"
)

// candidateRow 是候选池查询的行映射：clip 视图 + 计数 + 可空向量。
type candidateRow struct {
	ID           int64     `gorm:"column:id"`
	CreatorID    int64     `gorm:"column:creator_id"`
	PublishedAt  time.Time `gorm:"column:published_at"`
	LikeCount    int64     `gorm:"column:like_count"`
	SaveCount    int64     `gorm:"column:save_count"`
	CommentCount int64     `gorm:"column:comment_count"`
	PlayCount    int64     `gorm:"column:play_count"`
	Vector       *string   `gorm:"column:vector"`
}

// interestRow 是兴趣行为聚合的行映射：同一 clip 跨日曝光合并后的播放/完播计数。
type interestRow struct {
	ClipID         int64 `gorm:"column:clip_id"`
	PlayCount      int64 `gorm:"column:play_count"`
	CompletedCount int64 `gorm:"column:completed_count"`
}

// impressionRow 是曝光聚合查询的行映射。
type impressionRow struct {
	ViewerID       int64     `gorm:"column:viewer_id"`
	ClipID         int64     `gorm:"column:clip_id"`
	Day            string    `gorm:"column:day"`
	PlayCount      int64     `gorm:"column:play_count"`
	CompletedCount int64     `gorm:"column:completed_count"`
	FirstPlayedAt  time.Time `gorm:"column:first_played_at"`
	LastPlayedAt   time.Time `gorm:"column:last_played_at"`
}

// hotScoreExpr 是候选召回排序用的热度表达式，按 snapshotAt 计算年龄，保证候选池稳定。
const hotScoreExpr = "((COALESCE(s.like_count, 0) * 3 + COALESCE(s.save_count, 0) * 4 + " +
	"COALESCE(s.comment_count, 0) * 5 + COALESCE(s.play_count, 0)) / " +
	"POW(GREATEST(TIMESTAMPDIFF(SECOND, c.published_at, ?) / 3600.0, 0) + 2, 1.5))"

// Repository 是 recommendation 仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domrecommendation.Repository = (*Repository)(nil)

// New 创建 recommendation 仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// ListCandidates 按 snapshotAt 固定候选池：排除快照前已发布之外的 clip、排除最近 7 天曝光，
// 并按快照基准热度降序召回；向量缺失的候选仍参与排序，兴趣分按 0 处理。
func (r *Repository) ListCandidates(ctx context.Context, viewerID int64, snapshotAt time.Time, limit int) ([]domrecommendation.Candidate, error) {
	impressionSince := snapshotAt.Add(-time.Duration(domrecommendation.ImpressionWindowDays) * 24 * time.Hour)

	var rows []candidateRow
	err := r.db.WithContext(ctx).
		Table("pf_clips AS c").
		Select("c.id, c.creator_id, c.published_at, "+
			"COALESCE(s.like_count, 0) AS like_count, COALESCE(s.save_count, 0) AS save_count, "+
			"COALESCE(s.comment_count, 0) AS comment_count, COALESCE(s.play_count, 0) AS play_count, "+
			"e.vector AS vector, "+hotScoreExpr+" AS hot_score", snapshotAt).
		Joins("LEFT JOIN pf_clip_stats s ON s.clip_id = c.id").
		Joins("LEFT JOIN pf_clip_embeddings e ON e.clip_id = c.id").
		Where("c.status = ?", domclip.StatusPublished).
		Where("c.published_at <= ?", snapshotAt).
		Where("NOT EXISTS (SELECT 1 FROM pf_impressions i WHERE i.viewer_id = ? AND i.clip_id = c.id "+
			"AND i.last_played_at >= ? AND i.last_played_at <= ?)", viewerID, impressionSince, snapshotAt).
		Order("hot_score DESC, c.published_at DESC, c.id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	candidates := make([]domrecommendation.Candidate, 0, len(rows))
	for i := range rows {
		candidate := domrecommendation.Candidate{
			ClipID:       rows[i].ID,
			CreatorID:    rows[i].CreatorID,
			PublishedAt:  rows[i].PublishedAt,
			LikeCount:    rows[i].LikeCount,
			SaveCount:    rows[i].SaveCount,
			CommentCount: rows[i].CommentCount,
			PlayCount:    rows[i].PlayCount,
			Embedding:    decodeVector(rows[i].Vector),
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// ListInterestClips 返回读者 [since, until] 内曝光过的 clip 及跨日合并后的播放/完播计数。
func (r *Repository) ListInterestClips(ctx context.Context, viewerID int64, since, until time.Time, limit int) ([]domrecommendation.InterestClip, error) {
	var rows []interestRow
	err := r.db.WithContext(ctx).
		Table("pf_impressions AS i").
		Select("i.clip_id, SUM(i.play_count) AS play_count, SUM(i.completed_count) AS completed_count").
		Joins("JOIN pf_clip_embeddings e ON e.clip_id = i.clip_id").
		Where("i.viewer_id = ? AND i.last_played_at >= ? AND i.last_played_at <= ?", viewerID, since, until).
		Group("i.clip_id").
		Order("MAX(i.last_played_at) DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	clips := make([]domrecommendation.InterestClip, 0, len(rows))
	for i := range rows {
		clips = append(clips, domrecommendation.InterestClip{
			ClipID:         rows[i].ClipID,
			PlayCount:      rows[i].PlayCount,
			CompletedCount: rows[i].CompletedCount,
		})
	}
	return clips, nil
}

// GetImpression 读取读者对指定 clip 最近的一条曝光聚合记录。
func (r *Repository) GetImpression(ctx context.Context, viewerID, clipID int64) (*domrecommendation.ImpressionRecord, error) {
	var rows []impressionRow
	err := r.db.WithContext(ctx).
		Table("pf_impressions").
		Select("viewer_id, clip_id, day, play_count, completed_count, first_played_at, last_played_at").
		Where("viewer_id = ? AND clip_id = ?", viewerID, clipID).
		Order("last_played_at DESC").
		Limit(1).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, domrecommendation.ErrImpressionNotFound
	}
	return toImpressionRecord(&rows[0]), nil
}

// ListImpressions 返回读者 since 之后的曝光聚合记录，按最近播放时间降序。
func (r *Repository) ListImpressions(ctx context.Context, viewerID int64, since time.Time, limit int) ([]*domrecommendation.ImpressionRecord, error) {
	var rows []impressionRow
	err := r.db.WithContext(ctx).
		Table("pf_impressions").
		Select("viewer_id, clip_id, day, play_count, completed_count, first_played_at, last_played_at").
		Where("viewer_id = ? AND last_played_at >= ?", viewerID, since).
		Order("last_played_at DESC, clip_id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	records := make([]*domrecommendation.ImpressionRecord, 0, len(rows))
	for i := range rows {
		records = append(records, toImpressionRecord(&rows[i]))
	}
	return records, nil
}

// decodeVector 解析可空 JSON 向量列；缺失、解析失败或维度不符一律按 nil 处理。
func decodeVector(raw *string) domembedding.Vector {
	if raw == nil || *raw == "" {
		return nil
	}
	var values []float64
	if err := json.Unmarshal([]byte(*raw), &values); err != nil {
		return nil
	}
	vector, err := domembedding.NewVector(values)
	if err != nil {
		return nil
	}
	return vector
}

// toImpressionRecord 将曝光行转换为领域记录。
func toImpressionRecord(row *impressionRow) *domrecommendation.ImpressionRecord {
	return &domrecommendation.ImpressionRecord{
		ViewerID:       row.ViewerID,
		ClipID:         row.ClipID,
		Day:            row.Day,
		PlayCount:      row.PlayCount,
		CompletedCount: row.CompletedCount,
		FirstPlayedAt:  row.FirstPlayedAt,
		LastPlayedAt:   row.LastPlayedAt,
	}
}
