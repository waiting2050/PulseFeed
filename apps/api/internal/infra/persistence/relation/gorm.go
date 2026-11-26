// gorm.go 实现 relation 领域仓储端口：事务内按 ID 升序锁双方用户行 → 锁双方统计行 → 锁关系行，
// 仅在状态真正变化时维护 pf_user_stats 双向计数；列表联查资料表按复合游标分页。
package infrarelation

import (
	"context"
	"errors"
	"time"

	domrelation "pulsefeed/internal/domain/relation"
	infraaccount "pulsefeed/internal/infra/persistence/account"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 是 relation 仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domrelation.Repository = (*Repository)(nil)

// New 创建 relation 仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// SetFollow 把关注关系置为目标状态：状态变化才调整 pf_user_stats 双向计数，幂等命中仅刷新幂等键。
func (r *Repository) SetFollow(ctx context.Context, userID, targetUserID int64, follow bool, idempotencyKey string) (*domrelation.FollowResult, error) {
	desired := domrelation.StatusUnfollowed
	if follow {
		desired = domrelation.StatusFollowing
	}

	result := &domrelation.FollowResult{
		UserID:       userID,
		TargetUserID: targetUserID,
		Status:       desired,
		Following:    follow,
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 按 ID 升序锁双方用户行：互相关注并发时加锁顺序一致，避免死锁；
		//    同时校验双方存在（目标缺失映射为 ErrTargetNotFound）。
		if err := lockUsers(tx, userID, targetUserID); err != nil {
			return err
		}
		// 2. 按 ID 升序锁/补建双方统计行，后续计数变更在锁保护下读改写。
		stats, err := lockStatsPair(tx, userID, targetUserID)
		if err != nil {
			return err
		}
		// 3. 锁关系行。
		var relation FollowModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND target_user_id = ?", userID, targetUserID).
			Take(&relation).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// 首次建立关系：写入目标状态；仅“关注”才递增双向计数，首次取关保持计数稳定。
			model := FollowModel{
				UserID:         userID,
				TargetUserID:   targetUserID,
				Status:         desired,
				IdempotencyKey: idempotencyKey,
			}
			if err := tx.Create(&model).Error; err != nil {
				return err
			}
			if follow {
				if err := adjustCounts(tx, stats, 1); err != nil {
					return err
				}
			}
			// 首次取关只是补建行，未发生有效的状态迁移，不视为 Changed。
			result.Changed = follow
		case err != nil:
			return err
		default:
			if relation.Status == desired {
				// 状态未变化：幂等命中，仅记录最近一次幂等键。
				if idempotencyKey != "" && relation.IdempotencyKey != idempotencyKey {
					if err := tx.Model(&FollowModel{}).Where("id = ?", relation.ID).
						Update("idempotency_key", idempotencyKey).Error; err != nil {
						return err
					}
				}
			} else {
				updates := map[string]any{"status": desired}
				if idempotencyKey != "" {
					updates["idempotency_key"] = idempotencyKey
				}
				if err := tx.Model(&FollowModel{}).Where("id = ?", relation.ID).Updates(updates).Error; err != nil {
					return err
				}
				delta := int64(-1)
				if follow {
					delta = 1
				}
				if err := adjustCounts(tx, stats, delta); err != nil {
					return err
				}
				result.Changed = true
			}
		}

		result.FollowingCount = stats.self.FollowingCount
		result.FollowerCount = stats.target.FollowerCount
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListFollowing 按 (updated_at, target_user_id) 严格递减返回关注中的目标列表，联查资料表补展示字段。
func (r *Repository) ListFollowing(ctx context.Context, userID int64, cursor *domrelation.ListCursor, limit int) ([]*domrelation.ListEntry, error) {
	query := r.db.WithContext(ctx).
		Table("pf_follows AS f").
		Select("f.target_user_id AS user_id, f.updated_at AS followed_at, "+
			"p.nickname AS nickname, p.avatar_url AS avatar_url, p.bio AS bio").
		Joins("LEFT JOIN pf_user_profiles p ON p.user_id = f.target_user_id").
		Where("f.user_id = ? AND f.status = ?", userID, domrelation.StatusFollowing)
	if cursor != nil {
		// 复合游标：严格小于上一页最后一条的 (updated_at, target_user_id)。
		query = query.Where(
			"f.updated_at < ? OR (f.updated_at = ? AND f.target_user_id < ?)",
			cursor.FollowedAt, cursor.FollowedAt, cursor.UserID,
		)
	}

	var rows []listRow
	if err := query.Order("f.updated_at DESC, f.target_user_id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return toListEntries(rows), nil
}

// ListFollowers 按 (updated_at, user_id) 严格递减返回粉丝列表，联查资料表补展示字段。
func (r *Repository) ListFollowers(ctx context.Context, userID int64, cursor *domrelation.ListCursor, limit int) ([]*domrelation.ListEntry, error) {
	query := r.db.WithContext(ctx).
		Table("pf_follows AS f").
		Select("f.user_id AS user_id, f.updated_at AS followed_at, "+
			"p.nickname AS nickname, p.avatar_url AS avatar_url, p.bio AS bio").
		Joins("LEFT JOIN pf_user_profiles p ON p.user_id = f.user_id").
		Where("f.target_user_id = ? AND f.status = ?", userID, domrelation.StatusFollowing)
	if cursor != nil {
		// 复合游标：严格小于上一页最后一条的 (updated_at, user_id)。
		query = query.Where(
			"f.updated_at < ? OR (f.updated_at = ? AND f.user_id < ?)",
			cursor.FollowedAt, cursor.FollowedAt, cursor.UserID,
		)
	}

	var rows []listRow
	if err := query.Order("f.updated_at DESC, f.user_id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return toListEntries(rows), nil
}

// GetUserStats 读取用户的关注数与粉丝数；统计行不存在返回 0 值，不视为错误。
func (r *Repository) GetUserStats(ctx context.Context, userID int64) (*domrelation.UserStats, error) {
	var row UserStatsModel
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &domrelation.UserStats{}, nil
		}
		return nil, err
	}
	return &domrelation.UserStats{
		FollowingCount: row.FollowingCount,
		FollowerCount:  row.FollowerCount,
	}, nil
}

// IsFollowing 判断关注关系是否有效；自关注与非法 ID 直接返回 false。
func (r *Repository) IsFollowing(ctx context.Context, userID, targetUserID int64) (bool, error) {
	if userID <= 0 || targetUserID <= 0 || userID == targetUserID {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).
		Table("pf_follows").
		Where("user_id = ? AND target_user_id = ? AND status = ?", userID, targetUserID, domrelation.StatusFollowing).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// listRow 是关注/粉丝列表联查结果的行映射。
type listRow struct {
	UserID     int64
	FollowedAt time.Time
	Nickname   string
	AvatarURL  string
	Bio        string
}

// toListEntries 将联查行转换为领域列表条目。
func toListEntries(rows []listRow) []*domrelation.ListEntry {
	entries := make([]*domrelation.ListEntry, 0, len(rows))
	for i := range rows {
		entries = append(entries, &domrelation.ListEntry{
			UserID:     rows[i].UserID,
			Nickname:   rows[i].Nickname,
			AvatarURL:  rows[i].AvatarURL,
			Bio:        rows[i].Bio,
			FollowedAt: rows[i].FollowedAt,
		})
	}
	return entries
}

// statsPair 聚合一次关注写操作中双方用户的统计行。
type statsPair struct {
	self   *UserStatsModel
	target *UserStatsModel
}

// lockUsers 按 ID 升序以 FOR UPDATE 锁定双方用户行；任一不存在返回 ErrTargetNotFound。
func lockUsers(tx *gorm.DB, userID, targetUserID int64) error {
	ids := []int64{userID, targetUserID}
	if ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	for _, id := range ids {
		var user infraaccount.UserModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ?", id).Take(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domrelation.ErrTargetNotFound
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// lockStatsPair 按 ID 升序锁/补建双方统计行；缺失时补建 0 值行（老用户首次进入关系链路）。
func lockStatsPair(tx *gorm.DB, userID, targetUserID int64) (*statsPair, error) {
	ids := []int64{userID, targetUserID}
	if ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	locked := make(map[int64]*UserStatsModel, 2)
	for _, id := range ids {
		var stats UserStatsModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", id).Take(&stats).Error
		if err == nil {
			locked[id] = &stats
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		stats = UserStatsModel{UserID: id}
		if err := tx.Create(&stats).Error; err != nil {
			return nil, err
		}
		locked[id] = &stats
	}
	return &statsPair{self: locked[userID], target: locked[targetUserID]}, nil
}

// adjustCounts 按 delta 调整当前用户关注数与目标用户粉丝数并回写（下限 0）；
// 调用方已持有双方统计行锁，故可安全读改写。
func adjustCounts(tx *gorm.DB, stats *statsPair, delta int64) error {
	stats.self.FollowingCount += delta
	if stats.self.FollowingCount < 0 {
		stats.self.FollowingCount = 0
	}
	if err := tx.Model(&UserStatsModel{}).Where("user_id = ?", stats.self.UserID).
		UpdateColumn("following_count", stats.self.FollowingCount).Error; err != nil {
		return err
	}

	stats.target.FollowerCount += delta
	if stats.target.FollowerCount < 0 {
		stats.target.FollowerCount = 0
	}
	return tx.Model(&UserStatsModel{}).Where("user_id = ?", stats.target.UserID).
		UpdateColumn("follower_count", stats.target.FollowerCount).Error
}
