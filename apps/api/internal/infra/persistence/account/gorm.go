// gorm.go 实现 account 领域仓储端口：凭证与资料分表写入、唯一索引冲突转领域错误。
package infraaccount

import (
	"context"
	"errors"

	domainaccount "pulsefeed/internal/domain/account"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Repository 是 account 仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domainaccount.Repository = (*Repository)(nil)

// New 创建账号仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 在同一事务内写入 pf_users 与 pf_user_profiles，并回写自增 ID。
func (r *Repository) Create(ctx context.Context, account *domainaccount.Account, profile *domainaccount.UserProfile) error {
	user := UserModel{
		Account:      account.Name,
		PasswordHash: account.PasswordHash,
		Role:         account.Role,
		Status:       account.Status,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			if isDuplicateKeyError(err) {
				// uk_account 唯一索引冲突转换为领域错误，交由传输层映射 409。
				return domainaccount.ErrAccountExists
			}
			return err
		}

		profileModel := UserProfileModel{
			UserID:    user.ID,
			Nickname:  profile.Nickname,
			AvatarURL: profile.AvatarURL,
			Bio:       profile.Bio,
		}
		if err := tx.Create(&profileModel).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	account.ID = user.ID
	profile.UserID = user.ID
	return nil
}

// FindByAccount 按账号名精确查找凭证，登录流程使用。
func (r *Repository) FindByAccount(ctx context.Context, name string) (*domainaccount.Account, error) {
	var user UserModel
	err := r.db.WithContext(ctx).Where("account = ?", name).Take(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainaccount.ErrAccountNotFound
		}
		return nil, err
	}
	return domainaccount.RestoreAccount(user.ID, user.Account, user.PasswordHash, user.Role, user.Status), nil
}

// GetProfile 按用户 ID 读取展示资料。
func (r *Repository) GetProfile(ctx context.Context, userID int64) (*domainaccount.UserProfile, error) {
	var profile UserProfileModel
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Take(&profile).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainaccount.ErrAccountNotFound
		}
		return nil, err
	}
	return domainaccount.RestoreUserProfile(profile.UserID, profile.Nickname, profile.AvatarURL, profile.Bio), nil
}

// UpdateProfile 只更新资料展示字段，凭证、角色等字段保持原值。
func (r *Repository) UpdateProfile(ctx context.Context, profile *domainaccount.UserProfile) error {
	result := r.db.WithContext(ctx).
		Model(&UserProfileModel{}).
		Where("user_id = ?", profile.UserID).
		Updates(map[string]any{
			"nickname":   profile.Nickname,
			"avatar_url": profile.AvatarURL,
			"bio":        profile.Bio,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domainaccount.ErrAccountNotFound
	}
	return nil
}

// isDuplicateKeyError 判断是否为 MySQL 1062 唯一键冲突。
func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
