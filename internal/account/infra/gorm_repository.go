package infra

import (
	"context"
	"errors"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"pulsefeed/internal/account/domain"
)

type UserModel struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	Account      string    `gorm:"size:32;uniqueIndex;not null"`
	PasswordHash string    `gorm:"size:100;not null"`
	Nickname     string    `gorm:"size:32;not null"`
	AvatarURL    string    `gorm:"size:255;not null;default:''"`
	Bio          string    `gorm:"size:255;not null;default:''"`
	Role         string    `gorm:"size:16;not null;default:'user'"`
	Status       int8      `gorm:"not null;default:1"`
	CreatedAt    time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

func (UserModel) TableName() string {
	return "users"
}

type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

func (r *GormRepository) Create(ctx context.Context, user *domain.User) error {
	model := &UserModel{
		Account:      user.Account,
		PasswordHash: user.PasswordHash,
		Nickname:     user.Nickname,
		AvatarURL:    user.AvatarURL,
		Bio:          user.Bio,
		Role:         user.Role,
		Status:       int8(user.Status),
	}
	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		var mysqlErr *gomysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return domain.ErrAccountExists
		}
		return err
	}
	toDomain(model, user)
	return nil
}

func (r *GormRepository) FindByAccount(ctx context.Context, account string) (*domain.User, error) {
	var model UserModel
	err := r.db.WithContext(ctx).Where("account = ?", account).First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return toDomain(&model, nil), nil
}

func (r *GormRepository) FindByID(ctx context.Context, id int64) (*domain.User, error) {
	var model UserModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return toDomain(&model, nil), nil
}

func (r *GormRepository) UpdateProfile(ctx context.Context, id int64, nickname, avatarURL, bio string) error {
	result := r.db.WithContext(ctx).Model(&UserModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"nickname":   nickname,
			"avatar_url": avatarURL,
			"bio":        bio,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

func toDomain(model *UserModel, target *domain.User) *domain.User {
	if target == nil {
		target = &domain.User{}
	}
	target.ID = model.ID
	target.Account = model.Account
	target.PasswordHash = model.PasswordHash
	target.Nickname = model.Nickname
	target.AvatarURL = model.AvatarURL
	target.Bio = model.Bio
	target.Role = model.Role
	target.Status = int(model.Status)
	target.CreatedAt = model.CreatedAt
	target.UpdatedAt = model.UpdatedAt
	return target
}
