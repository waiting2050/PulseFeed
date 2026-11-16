// gorm.go 实现互动领域仓储端口：点赞/收藏以唯一索引做幂等判定并原子增减计数，
// 不锁定 pf_clips；评论写入/软删除同步维护 comment_count。
package infraaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domclip "pulsefeed/internal/domain/clip"
	dominteraction "pulsefeed/internal/domain/interaction"
	infraaccount "pulsefeed/internal/infra/persistence/account"
	infraclip "pulsefeed/internal/infra/persistence/clip"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 是互动仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ dominteraction.Repository = (*Repository)(nil)

// New 创建互动仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// SetAction 把行为置为目标状态：由 uk_user_clip_type 唯一索引承担幂等，
// 一次条件插入的影响行数即可区分“首次写入 / 状态翻转 / 重复操作”，
// 不再先查后写，也不再对 pf_clips 加行锁；计数由数据库原子增减。
func (r *Repository) SetAction(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, error) {
	if !actionType.Valid() {
		return nil, dominteraction.ErrInvalidActionType
	}
	desired := dominteraction.StatusCanceled
	if active {
		desired = dominteraction.StatusActive
	}

	result := &dominteraction.ActionResult{ClipID: clipID, ActionType: actionType, Active: active}
	// READ COMMITTED 下条件插入对 pf_clips 只做一致性读、不取共享锁，
	// 避免为了“作品可互动”校验重新引入行锁。
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1 = 新增行为，2 = 已有行为状态翻转，0 = 重复操作或作品不可互动。
		outcome := tx.Exec(
			`INSERT INTO pf_actions (user_id, clip_id, action_type, status, idempotency_key, created_at, updated_at)
SELECT ?, ?, ?, ?, ?, CURRENT_TIMESTAMP(3), CURRENT_TIMESTAMP(3) FROM pf_clips WHERE id = ? AND status = ?
ON DUPLICATE KEY UPDATE
  idempotency_key = IF(pf_actions.status <> ?, IF(? = '', pf_actions.idempotency_key, ?), pf_actions.idempotency_key),
  updated_at = IF(pf_actions.status <> ?, CURRENT_TIMESTAMP(3), pf_actions.updated_at),
  status = ?`,
			userID, clipID, string(actionType), desired, idempotencyKey, clipID, domclip.StatusPublished,
			desired, idempotencyKey, idempotencyKey, desired, desired,
		)
		if outcome.Error != nil {
			return outcome.Error
		}

		switch outcome.RowsAffected {
		case 1:
			// 首次行为：仅“启用”递增计数，首次取消保持计数稳定。
			result.Changed = active
			if active {
				if err := adjustStats(tx, clipID, actionType, 1); err != nil {
					return err
				}
			}
		case 2:
			// 已有行为状态翻转：按方向原子增减。
			delta := int64(-1)
			if active {
				delta = 1
			}
			if err := adjustStats(tx, clipID, actionType, delta); err != nil {
				return err
			}
			result.Changed = true
		default:
			// 影响 0 行：要么状态本就一致（幂等），要么作品不存在/非发布态。
			return fillUnchangedResult(tx, clipID, result)
		}

		like, save, err := readStats(tx, clipID)
		if err != nil {
			return err
		}
		result.LikeCount = like
		result.SaveCount = save
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ExistsPublished 判断 clip 是否存在且处于发布态，只做一次轻量只读查询。
func (r *Repository) ExistsPublished(ctx context.Context, clipID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&infraclip.ClipModel{}).
		Where("id = ? AND status = ?", clipID, domclip.StatusPublished).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateComment 写入评论并递增 comment_count；同 (user_id, 幂等键) 命中时返回既有评论。
func (r *Repository) CreateComment(ctx context.Context, comment *dominteraction.Comment, idempotencyKey string) (*dominteraction.Comment, int64, error) {
	var saved *dominteraction.Comment
	var count int64

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockPublishedClip(tx, comment.ClipID); err != nil {
			return err
		}
		stats, err := lockStats(tx, comment.ClipID)
		if err != nil {
			return err
		}

		// 幂等命中：直接返回既有评论，不再递增计数。
		if idempotencyKey != "" {
			var existing CommentModel
			findErr := tx.Where("user_id = ? AND idempotency_key = ?", comment.UserID, idempotencyKey).
				Take(&existing).Error
			if findErr == nil {
				saved = toComment(&existing)
				fillProfile(tx, saved)
				count = stats.CommentCount
				return nil
			}
			if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return findErr
			}
		}

		model := CommentModel{
			ClipID:         comment.ClipID,
			UserID:         comment.UserID,
			Content:        comment.Content,
			Status:         dominteraction.CommentStatusVisible,
			IdempotencyKey: idempotencyKeyPtr(idempotencyKey),
		}
		if err := tx.Create(&model).Error; err != nil {
			if isDuplicateKeyError(err) {
				// 并发同键插入：回读既有评论，计数保持不变。
				var existing CommentModel
				if findErr := tx.Where("user_id = ? AND idempotency_key = ?", comment.UserID, idempotencyKey).
					Take(&existing).Error; findErr != nil {
					return findErr
				}
				saved = toComment(&existing)
				fillProfile(tx, saved)
				count = stats.CommentCount
				return nil
			}
			return err
		}

		stats.CommentCount++
		if err := tx.Model(&infraclip.ClipStatsModel{}).Where("clip_id = ?", stats.ClipID).
			UpdateColumn("comment_count", stats.CommentCount).Error; err != nil {
			return err
		}
		saved = toComment(&model)
		fillProfile(tx, saved)
		count = stats.CommentCount
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return saved, count, nil
}

// DeleteComment 软删除评论并递减 comment_count；重复删除幂等返回当前结果。
func (r *Repository) DeleteComment(ctx context.Context, requesterID, commentID int64) (*dominteraction.DeletedComment, error) {
	result := &dominteraction.DeletedComment{CommentID: commentID}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model CommentModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", commentID).Take(&model).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dominteraction.ErrCommentNotFound
		}
		if err != nil {
			return err
		}

		// 权限：评论作者本人或所属 clip 的创作者可删除。
		var clip infraclip.ClipModel
		if err := tx.Select("id", "creator_id").Where("id = ?", model.ClipID).Take(&clip).Error; err != nil &&
			!errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if model.UserID != requesterID && clip.CreatorID != requesterID {
			return dominteraction.ErrCommentPermissionDenied
		}

		stats, err := lockStats(tx, model.ClipID)
		if err != nil {
			return err
		}
		result.Status = model.Status
		result.CommentCount = stats.CommentCount
		if model.Status == dominteraction.CommentStatusDeleted {
			// 已是删除态：幂等返回，计数保持稳定。
			return nil
		}

		if err := tx.Model(&CommentModel{}).Where("id = ?", model.ID).
			Update("status", dominteraction.CommentStatusDeleted).Error; err != nil {
			return err
		}
		if stats.CommentCount > 0 {
			stats.CommentCount--
		}
		if err := tx.Model(&infraclip.ClipStatsModel{}).Where("clip_id = ?", stats.ClipID).
			UpdateColumn("comment_count", stats.CommentCount).Error; err != nil {
			return err
		}
		result.Status = dominteraction.CommentStatusDeleted
		result.CommentCount = stats.CommentCount
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListComments 按 (created_at, id) 严格递减返回可见评论，联查资料表补昵称与头像。
func (r *Repository) ListComments(ctx context.Context, clipID int64, cursor *dominteraction.CommentCursor, limit int) ([]*dominteraction.Comment, error) {
	query := r.db.WithContext(ctx).
		Table("pf_comments AS c").
		Select("c.id, c.clip_id, c.user_id, c.content, c.status, c.created_at, "+
			"p.nickname AS nickname, p.avatar_url AS avatar_url").
		Joins("LEFT JOIN pf_user_profiles p ON p.user_id = c.user_id").
		Where("c.clip_id = ? AND c.status = ?", clipID, dominteraction.CommentStatusVisible)
	if cursor != nil {
		query = query.Where(
			"c.created_at < ? OR (c.created_at = ? AND c.id < ?)",
			cursor.CreatedAt, cursor.CreatedAt, cursor.CommentID,
		)
	}

	var rows []commentRow
	if err := query.Order("c.created_at DESC, c.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}

	comments := make([]*dominteraction.Comment, 0, len(rows))
	for i := range rows {
		comments = append(comments, &dominteraction.Comment{
			ID:        rows[i].ID,
			ClipID:    rows[i].ClipID,
			UserID:    rows[i].UserID,
			Nickname:  rows[i].Nickname,
			AvatarURL: rows[i].AvatarURL,
			Content:   rows[i].Content,
			Status:    rows[i].Status,
			CreatedAt: rows[i].CreatedAt,
		})
	}
	return comments, nil
}

// commentRow 是评论列表联查结果的行映射。
type commentRow struct {
	ID        int64
	ClipID    int64
	UserID    int64
	Content   string
	Status    int
	CreatedAt time.Time
	Nickname  string
	AvatarURL string
}

// lockPublishedClip 以 FOR UPDATE 锁定 clip 行并校验其存在且为发布态。
func lockPublishedClip(tx *gorm.DB, clipID int64) error {
	var clip infraclip.ClipModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").Where("id = ?", clipID).Take(&clip).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return dominteraction.ErrClipNotFound
	}
	if err != nil {
		return err
	}
	if clip.Status != domclip.StatusPublished {
		return dominteraction.ErrClipNotFound
	}
	return nil
}

// lockStats 以 FOR UPDATE 锁定计数行；缺失时补建 0 值行（正常发布路径不会发生）。
func lockStats(tx *gorm.DB, clipID int64) (*infraclip.ClipStatsModel, error) {
	var stats infraclip.ClipStatsModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("clip_id = ?", clipID).Take(&stats).Error
	if err == nil {
		return &stats, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	stats = infraclip.ClipStatsModel{ClipID: clipID}
	if err := tx.Create(&stats).Error; err != nil {
		return nil, err
	}
	return &stats, nil
}

// adjustStats 用单条 upsert 原子增减计数，不再先锁行再读改写：
// 计数行缺失时按增量建行（负增量归零），存在时在数据库侧完成增减并保证下限 0。
func adjustStats(tx *gorm.DB, clipID int64, actionType dominteraction.ActionType, delta int64) error {
	column := "like_count"
	if actionType == dominteraction.ActionSave {
		column = "save_count"
	}
	stmt := fmt.Sprintf(
		`INSERT INTO pf_clip_stats (clip_id, %s, created_at, updated_at)
VALUES (?, GREATEST(?, 0), CURRENT_TIMESTAMP(3), CURRENT_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE %s = GREATEST(%s + ?, 0), updated_at = CURRENT_TIMESTAMP(3)`,
		column, column, column,
	)
	return tx.Exec(stmt, clipID, delta, delta).Error
}

// fillUnchangedResult 处理条件插入影响 0 行的情况：
// 作品不存在或非发布态返回 ErrClipNotFound，否则是重复操作，直接回填当前计数。
func fillUnchangedResult(tx *gorm.DB, clipID int64, result *dominteraction.ActionResult) error {
	var snapshot struct {
		Status    int
		LikeCount int64
		SaveCount int64
	}
	err := tx.Raw(
		`SELECT c.status AS status,
       COALESCE(s.like_count, 0) AS like_count,
       COALESCE(s.save_count, 0) AS save_count
FROM pf_clips c
LEFT JOIN pf_clip_stats s ON s.clip_id = c.id
WHERE c.id = ?`, clipID,
	).Scan(&snapshot).Error
	if err != nil {
		return err
	}
	if snapshot.Status != domclip.StatusPublished {
		return dominteraction.ErrClipNotFound
	}
	result.LikeCount = snapshot.LikeCount
	result.SaveCount = snapshot.SaveCount
	return nil
}

// readStats 读取最新计数；计数行缺失时按 0 处理。
func readStats(tx *gorm.DB, clipID int64) (int64, int64, error) {
	var stats infraclip.ClipStatsModel
	err := tx.Select("like_count", "save_count").Where("clip_id = ?", clipID).Take(&stats).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return stats.LikeCount, stats.SaveCount, nil
}

// fillProfile 为评论补充作者昵称与头像；资料缺失时保留空值，不影响评论写入。
func fillProfile(tx *gorm.DB, comment *dominteraction.Comment) {
	var profile infraaccount.UserProfileModel
	if err := tx.Where("user_id = ?", comment.UserID).Take(&profile).Error; err != nil {
		return
	}
	comment.Nickname = profile.Nickname
	comment.AvatarURL = profile.AvatarURL
}

// toComment 将 GORM 模型转换为领域评论实体。
func toComment(m *CommentModel) *dominteraction.Comment {
	return &dominteraction.Comment{
		ID:        m.ID,
		ClipID:    m.ClipID,
		UserID:    m.UserID,
		Content:   m.Content,
		Status:    m.Status,
		CreatedAt: m.CreatedAt,
	}
}

// idempotencyKeyPtr 把空幂等键归一为 NULL，避免唯一索引把多条无键评论判为冲突。
func idempotencyKeyPtr(key string) *string {
	if key == "" {
		return nil
	}
	return &key
}

// isDuplicateKeyError 判断是否为 MySQL 1062 唯一键冲突。
func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
