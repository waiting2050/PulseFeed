// model.go 定义 account 模块 pf_users 与 pf_user_profiles 两张表的 GORM 模型。
package infraaccount

import "time"

// UserModel 映射 pf_users 表，保存登录凭证与权限字段；账号名带 uk_account 唯一索引。
type UserModel struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Account      string    `gorm:"column:account;size:64;not null;uniqueIndex:uk_account"`
	PasswordHash string    `gorm:"column:password_hash;size:255;not null"`
	Role         string    `gorm:"column:role;size:32;not null;default:user"`
	Status       int       `gorm:"column:status;not null;default:1"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定凭证表名。
func (UserModel) TableName() string {
	return "pf_users"
}

// UserProfileModel 映射 pf_user_profiles 表，与凭证分表存放展示资料，主键即用户 ID。
type UserProfileModel struct {
	UserID    int64     `gorm:"column:user_id;primaryKey"`
	Nickname  string    `gorm:"column:nickname;size:64;not null"`
	AvatarURL string    `gorm:"column:avatar_url;size:512"`
	Bio       string    `gorm:"column:bio;size:255"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定资料表名。
func (UserProfileModel) TableName() string {
	return "pf_user_profiles"
}
