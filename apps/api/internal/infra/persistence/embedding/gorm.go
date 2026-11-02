// gorm.go 实现 embedding 领域仓储：读取 clip 标题、向量 upsert 与批量读取。
package infraembedding

import (
	"context"
	"encoding/json"

	domclip "pulsefeed/internal/domain/clip"
	domembedding "pulsefeed/internal/domain/embedding"

	"gorm.io/gorm"
)

// embeddingRow 是 pf_clip_embeddings 批量读取的行映射。
type embeddingRow struct {
	ClipID int64  `gorm:"column:clip_id"`
	Vector string `gorm:"column:vector"`
}

// Repository 是 embedding 仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domembedding.Repository = (*Repository)(nil)

// New 创建 embedding 仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// GetClipTitle 读取已发布 clip 的标题；不存在或非发布态返回 ErrClipNotFound。
func (r *Repository) GetClipTitle(ctx context.Context, clipID int64) (string, error) {
	var titles []string
	err := r.db.WithContext(ctx).
		Table("pf_clips").
		Where("id = ? AND status = ?", clipID, domclip.StatusPublished).
		Limit(1).
		Pluck("title", &titles).Error
	if err != nil {
		return "", err
	}
	if len(titles) == 0 {
		return "", domembedding.ErrClipNotFound
	}
	return titles[0], nil
}

// Upsert 写入或覆盖 clip 向量：主键冲突时更新维度、向量与时间戳。
func (r *Repository) Upsert(ctx context.Context, clipID int64, vector domembedding.Vector) error {
	if len(vector) != domembedding.Dimension {
		return domembedding.ErrInvalidDimension
	}
	encoded, err := json.Marshal(vector)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).
		Exec("INSERT INTO pf_clip_embeddings (clip_id, dim, vector, updated_at) VALUES (?, ?, ?, NOW()) "+
			"ON DUPLICATE KEY UPDATE dim = VALUES(dim), vector = VALUES(vector), updated_at = NOW()",
			clipID, domembedding.Dimension, string(encoded)).Error
}

// BatchGet 批量读取向量：JSON 解析失败或维度不符的行跳过，保证候选排序可降级为无兴趣特征。
func (r *Repository) BatchGet(ctx context.Context, clipIDs []int64) (map[int64]domembedding.Vector, error) {
	found := make(map[int64]domembedding.Vector, len(clipIDs))
	if len(clipIDs) == 0 {
		return found, nil
	}

	var rows []embeddingRow
	err := r.db.WithContext(ctx).
		Table("pf_clip_embeddings").
		Select("clip_id, vector").
		Where("clip_id IN ?", clipIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for i := range rows {
		var values []float64
		if err := json.Unmarshal([]byte(rows[i].Vector), &values); err != nil {
			continue
		}
		vector, err := domembedding.NewVector(values)
		if err != nil {
			continue
		}
		found[rows[i].ClipID] = vector
	}
	return found, nil
}
