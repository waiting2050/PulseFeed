// gorm.go 实现 clip 领域仓储端口：事务写两表、唯一索引冲突转领域错误、复合游标分页。
package infraclip

import (
	"context"
	"errors"

	domclip "pulsefeed/internal/domain/clip"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Repository 是 clip 仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domclip.Repository = (*Repository)(nil)

// New 创建 clip 仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 在同一事务内写入 pf_clips 与 pf_clip_stats（0 值计数行），并回写自增 ID。
func (r *Repository) Create(ctx context.Context, c *domclip.Clip) error {
	model := ClipModel{
		CreatorID:      c.CreatorID,
		Title:          c.Title,
		Description:    c.Description,
		VideoURL:       c.VideoURL,
		CoverURL:       c.CoverURL,
		DurationMS:     c.DurationMS,
		Status:         c.Status,
		IdempotencyKey: c.IdempotencyKey,
		PublishedAt:    c.PublishedAt,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model).Error; err != nil {
			if isDuplicateKeyError(err) {
				// uk_creator_idempotency 冲突转换为领域错误，应用层负责回读已存在记录。
				return domclip.ErrClipExists
			}
			return err
		}
		if err := tx.Create(&ClipStatsModel{ClipID: model.ID}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	c.ID = model.ID
	return nil
}

// GetByID 按主键读取 clip，不过滤删除状态，可见性由应用层判定。
func (r *Repository) GetByID(ctx context.Context, id int64) (*domclip.Clip, error) {
	var model ClipModel
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domclip.ErrClipNotFound
		}
		return nil, err
	}
	return toEntity(&model), nil
}

// GetByIdempotencyKey 按 (creator_id, idempotency_key) 查重，幂等发布流程使用。
func (r *Repository) GetByIdempotencyKey(ctx context.Context, creatorID int64, idempotencyKey string) (*domclip.Clip, error) {
	var model ClipModel
	err := r.db.WithContext(ctx).
		Where("creator_id = ? AND idempotency_key = ?", creatorID, idempotencyKey).
		Take(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domclip.ErrClipNotFound
		}
		return nil, err
	}
	return toEntity(&model), nil
}

// ListByCreator 按 (published_at, id) 严格递减分页返回创作者可见 clip。
func (r *Repository) ListByCreator(ctx context.Context, creatorID int64, cursor *domclip.Cursor, limit int) ([]*domclip.Clip, error) {
	query := r.db.WithContext(ctx).
		Where("creator_id = ? AND status = ?", creatorID, domclip.StatusPublished)
	if cursor != nil {
		// 复合游标：严格小于上一页最后一条的 (published_at, id)，保证不重不漏。
		query = query.Where(
			"published_at < ? OR (published_at = ? AND id < ?)",
			cursor.PublishedAt, cursor.PublishedAt, cursor.ClipID,
		)
	}

	var models []ClipModel
	err := query.
		Order("published_at DESC, id DESC").
		Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	clips := make([]*domclip.Clip, 0, len(models))
	for i := range models {
		clips = append(clips, toEntity(&models[i]))
	}
	return clips, nil
}

// Delete 将 clip 置为已删除；重复删除幂等返回 nil，不存在返回 ErrClipNotFound。
func (r *Repository) Delete(ctx context.Context, id int64, _ string) error {
	result := r.db.WithContext(ctx).
		Model(&ClipModel{}).
		Where("id = ? AND status <> ?", id, domclip.StatusDeleted).
		Update("status", domclip.StatusDeleted)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}

	// 未更新任何行：区分“不存在”与“已是删除态”（后者幂等成功）。
	var model ClipModel
	err := r.db.WithContext(ctx).Select("id").Where("id = ?", id).Take(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domclip.ErrClipNotFound
		}
		return err
	}
	return nil
}

// toEntity 将 GORM 模型转换为领域实体。
func toEntity(m *ClipModel) *domclip.Clip {
	return domclip.RestoreClip(m.ID, m.CreatorID, m.Title, m.Description, m.VideoURL, m.CoverURL, m.DurationMS, m.Status, m.IdempotencyKey, m.PublishedAt)
}

// isDuplicateKeyError 判断是否为 MySQL 1062 唯一键冲突。
func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
