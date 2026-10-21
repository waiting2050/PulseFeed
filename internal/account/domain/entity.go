package domain

import (
	"errors"
	"time"
)

const (
	RoleUser   = "user"
	StatusOK   = 1
	StatusDown = 0
)

var (
	ErrAccountExists      = errors.New("账号已存在")
	ErrInvalidCredentials = errors.New("账号或密码错误")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrInvalidAccount     = errors.New("账号需为 4-32 位字母、数字或下划线")
	ErrInvalidPassword    = errors.New("密码长度需为 8-64 位")
	ErrInvalidNickname    = errors.New("昵称长度需为 1-32 位")
	ErrInvalidProfile     = errors.New("资料内容超出长度限制")
)

type User struct {
	ID           int64
	Account      string
	PasswordHash string
	Nickname     string
	AvatarURL    string
	Bio          string
	Role         string
	Status       int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (u *User) IsActive() bool {
	return u.Status == StatusOK
}
