// entity.go 定义 account 领域的 Account 与 UserProfile 实体及其校验规则。
package domainaccount

import (
	"strings"
	"unicode/utf8"
)

const (
	// StatusNormal 表示账号处于正常可用状态。
	StatusNormal = 1
	// RoleUser 是普通用户的默认角色。
	RoleUser = "user"

	// 账号名长度限制（按字节计，规范化后判定）。
	accountNameMinLen = 3
	accountNameMaxLen = 64
	// 昵称长度上限（按字符/rune 计；列宽 64 字符）。
	nicknameMaxLen = 64
)

// Account 是账号聚合根，只保存登录凭证与权限字段，展示资料归 UserProfile。
type Account struct {
	ID           int64
	Name         string
	PasswordHash string
	Role         string
	Status       int
}

// NormalizeAccountName 规范化账号名：去首尾空白并统一小写，保证唯一索引命中一致。
func NormalizeAccountName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateAccountName 校验规范化后的账号名：非空且长度在 [3, 64] 字节之间。
func ValidateAccountName(name string) error {
	if name == "" || len(name) < accountNameMinLen || len(name) > accountNameMaxLen {
		return ErrInvalidAccountName
	}
	return nil
}

// NewAccount 创建新账号，负责账号名规范化与校验；密码哈希由应用层生成后传入。
func NewAccount(name, passwordHash string) (*Account, error) {
	normalized := NormalizeAccountName(name)
	if err := ValidateAccountName(normalized); err != nil {
		return nil, err
	}
	return &Account{
		Name:         normalized,
		PasswordHash: passwordHash,
		Role:         RoleUser,
		Status:       StatusNormal,
	}, nil
}

// RestoreAccount 从持久化记录恢复账号实体，读取路径不再执行注册校验。
func RestoreAccount(id int64, name, passwordHash, role string, status int) *Account {
	return &Account{
		ID:           id,
		Name:         NormalizeAccountName(name),
		PasswordHash: passwordHash,
		Role:         role,
		Status:       status,
	}
}

// UserProfile 是账号的展示资料，与登录凭证分表存储。
type UserProfile struct {
	UserID    int64
	Nickname  string
	AvatarURL string
	Bio       string
}

// NewUserProfile 创建资料，昵称去空白后必填且长度受限。
func NewUserProfile(userID int64, nickname string) (*UserProfile, error) {
	nickname = strings.TrimSpace(nickname)
	if err := ValidateNickname(nickname); err != nil {
		return nil, err
	}
	return &UserProfile{UserID: userID, Nickname: nickname}, nil
}

// ValidateNickname 校验昵称：非空且不超过长度上限。
func ValidateNickname(nickname string) error {
	if nickname == "" || utf8.RuneCountInString(nickname) > nicknameMaxLen {
		return ErrInvalidNickname
	}
	return nil
}

// RestoreUserProfile 从持久化记录恢复资料实体。
func RestoreUserProfile(userID int64, nickname, avatarURL, bio string) *UserProfile {
	return &UserProfile{
		UserID:    userID,
		Nickname:  strings.TrimSpace(nickname),
		AvatarURL: strings.TrimSpace(avatarURL),
		Bio:       strings.TrimSpace(bio),
	}
}

// Update 执行资料部分更新，指针为 nil 表示该字段保持原值；三字段全 nil 视为空更新。
func (p *UserProfile) Update(nickname, avatarURL, bio *string) error {
	if nickname == nil && avatarURL == nil && bio == nil {
		return ErrEmptyProfileUpdate
	}
	if nickname != nil {
		value := strings.TrimSpace(*nickname)
		if err := ValidateNickname(value); err != nil {
			return err
		}
		p.Nickname = value
	}
	if avatarURL != nil {
		p.AvatarURL = strings.TrimSpace(*avatarURL)
	}
	if bio != nil {
		p.Bio = strings.TrimSpace(*bio)
	}
	return nil
}
