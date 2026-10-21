package infra

import (
	"context"
	"errors"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	accountdomain "pulsefeed/internal/account/domain"
)

type RelationModel struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"`
	FollowerID int64     `gorm:"not null;uniqueIndex:uk_follower_followee,priority:1;index:idx_follower_created,priority:1"`
	FolloweeID int64     `gorm:"not null;uniqueIndex:uk_follower_followee,priority:2;index:idx_followee,priority:1"`
	CreatedAt  time.Time `gorm:"not null;index:idx_follower_created,priority:2"`
}

func (RelationModel) TableName() string {
	return "user_relations"
}

type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

func (r *GormRepository) UserExists(ctx context.Context, userID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Table("users").
		Where("id = ? AND status = ?", userID, accountdomain.StatusOK).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *GormRepository) Follow(ctx context.Context, followerID, followeeID int64) (bool, error) {
	model := &RelationModel{FollowerID: followerID, FolloweeID: followeeID}
	err := r.db.WithContext(ctx).Create(model).Error
	if err != nil {
		var mysqlErr *gomysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *GormRepository) Unfollow(ctx context.Context, followerID, followeeID int64) (bool, error) {
	result := r.db.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Delete(&RelationModel{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *GormRepository) ListFolloweeIDs(ctx context.Context, followerID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		return nil, nil
	}

	ids := make([]int64, 0, limit)
	err := r.db.WithContext(ctx).
		Model(&RelationModel{}).
		Where("follower_id = ?", followerID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Pluck("followee_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}
