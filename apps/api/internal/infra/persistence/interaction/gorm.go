// gorm.go 实现互动领域仓储端口：事务内锁 clip → 锁计数行 → 锁行为行，
// 仅在状态真正变化时调整计数；评论写入/软删除同步维护 comment_count。
package infraaction

import (
	"context"
	"errors"
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

// SetAction 把行为置为目标状态：状态变化才调整 pf_clip_stats，幂等命中仅刷新幂等键。
func (r *Repository) SetAction(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, error) {
	if !actionType.Valid() {
		return nil, dominteraction.ErrInvalidActionType
	}
	desired := dominteraction.StatusCanceled
	if active {
		desired = dominteraction.StatusActive
	}

	result := &dominteraction.ActionResult{ClipID: clipID, ActionType: actionType, Active: active}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 锁 clip 行：既校验可见性，也把同一 clip 的并发互动写入串行化，
		//    这样“无记录→插入”的竞争不会发生，计数不会重复累加。
		if err := lockPublishedClip(tx, clipID); err != nil {
			return err
		}
		// 2. 锁计数行，后续计数变更在锁保护下读改写。
		stats, err := lockStats(tx, clipID)
		if err != nil {
			return err
		}
		// 3. 锁行为行。
		var action ActionModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND clip_id = ? AND action_type = ?", userID, clipID, string(actionType)).
			Take(&action).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// 首次行为：写入目标状态；仅“启用”才递增计数，首次取消保持计数稳定。
			model := ActionModel{
				UserID:         userID,
				ClipID:         clipID,
				ActionType:     string(actionType),
				Status:         desired,
				IdempotencyKey: idempotencyKey,
			}
			if err := tx.Create(&model).Error; err != nil {
				return err
			}
			if active {
				if err := adjustStats(tx, stats, actionType, 1); err != nil {
					return err
				}
			}
		case err != nil:
			return err
		default:
			if action.Status == desired {
				// 状态未变化：幂等命中，仅记录最近一次幂等键。
				if idempotencyKey != "" && action.IdempotencyKey != idempotencyKey {
					if err := tx.Model(&ActionModel{}).Where("id = ?", action.ID).
						Update("idempotency_key", idempotencyKey).Error; err != nil {
						return err
					}
				}
			} else {
				updates := map[string]any{"status": desired}
				if idempotencyKey != "" {
					updates["idempotency_key"] = idempotencyKey
				}
				if err := tx.Model(&ActionModel{}).Where("id = ?", action.ID).Updates(updates).Error; err != nil {
					return err
				}
				delta := int64(-1)
				if active {
					delta = 1
				}
				if err := adjustStats(tx, stats, actionType, delta); err != nil {
					return err
				}
			}
		}

		result.LikeCount = stats.LikeCount
		result.SaveCount = stats.SaveCount
		return nil
	})
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

// adjustStats 按行为类型调整计数并回写；调用方已持有计数行锁，故可安全读改写。
func adjustStats(tx *gorm.DB, stats *infraclip.ClipStatsModel, actionType dominteraction.ActionType, delta int64) error {
	column := "like_count"
	if actionType == dominteraction.ActionSave {
		column = "save_count"
	}
	value := stats.LikeCount + delta
	if column == "save_count" {
		value = stats.SaveCount + delta
	}
	if value < 0 {
		value = 0
	}
	if column == "save_count" {
		stats.SaveCount = value
	} else {
		stats.LikeCount = value
	}
	return tx.Model(&infraclip.ClipStatsModel{}).Where("clip_id = ?", stats.ClipID).
		UpdateColumn(column, value).Error
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
