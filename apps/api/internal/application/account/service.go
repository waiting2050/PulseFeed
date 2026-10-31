// service.go 编排账号注册、登录、资料读写与登出用例，只依赖领域端口与令牌签发端口。
package appaccount

import (
	"context"
	"errors"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"golang.org/x/crypto/bcrypt"
)

// bcrypt 哈希输入上限为 72 字节，超长密码直接判定为非法凭证。
const maxPasswordBytes = 72

// TokenIssuer 是应用层的令牌签发端口，由 infra/jwt 实现并经组合根注入。
type TokenIssuer interface {
	// Issue 为指定用户签发访问令牌。
	Issue(userID int64, role string) (string, error)
	// AccessTTL 返回令牌有效期，用于计算 expires_in。
	AccessTTL() time.Duration
}

// Profile 是面向传输层的资料视图。
type Profile struct {
	UserID    int64
	Account   string
	Nickname  string
	AvatarURL string
	Bio       string
}

// Session 是登录成功后的会话视图，携带访问令牌。
type Session struct {
	Profile
	AccessToken string
	ExpiresIn   int64
}

// Option 用于装配可选依赖。
type Option func(*Service)

// WithTokenIssuer 注入令牌签发器，登录用例必需。
func WithTokenIssuer(issuer TokenIssuer) Option {
	return func(s *Service) {
		s.issuer = issuer
	}
}

// Service 提供账号相关用例。
type Service struct {
	repo   domainaccount.Repository
	issuer TokenIssuer
}

// New 创建账号应用服务。
func New(repo domainaccount.Repository, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register 注册新账号：bcrypt 哈希密码，事务写入凭证与资料；昵称为空时回退为账号名。
func (s *Service) Register(ctx context.Context, account, password, nickname string) (*Profile, error) {
	if password == "" || len(password) > maxPasswordBytes {
		return nil, domainaccount.ErrInvalidCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	acc, err := domainaccount.NewAccount(account, string(hash))
	if err != nil {
		return nil, err
	}
	if nickname == "" {
		nickname = acc.Name
	}
	profile, err := domainaccount.NewUserProfile(0, nickname)
	if err != nil {
		return nil, err
	}

	if err := s.repo.Create(ctx, acc, profile); err != nil {
		return nil, err
	}
	return &Profile{
		UserID:    acc.ID,
		Account:   acc.Name,
		Nickname:  profile.Nickname,
		AvatarURL: profile.AvatarURL,
		Bio:       profile.Bio,
	}, nil
}

// Login 校验账号密码并签发访问令牌；账号不存在与密码错误统一返回 ErrInvalidCredentials。
func (s *Service) Login(ctx context.Context, account, password string) (*Session, error) {
	if s.issuer == nil {
		return nil, errors.New("token issuer 未注入")
	}
	acc, err := s.repo.FindByAccount(ctx, domainaccount.NormalizeAccountName(account))
	if err != nil {
		if errors.Is(err, domainaccount.ErrAccountNotFound) {
			return nil, domainaccount.ErrInvalidCredentials
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(password)); err != nil {
		return nil, domainaccount.ErrInvalidCredentials
	}

	token, err := s.issuer.Issue(acc.ID, acc.Role)
	if err != nil {
		return nil, err
	}
	return &Session{
		Profile:     Profile{UserID: acc.ID, Account: acc.Name},
		AccessToken: token,
		ExpiresIn:   int64(s.issuer.AccessTTL().Seconds()),
	}, nil
}

// Me 读取指定用户的展示资料。
func (s *Service) Me(ctx context.Context, userID int64) (*Profile, error) {
	profile, err := s.repo.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Profile{
		UserID:    profile.UserID,
		Nickname:  profile.Nickname,
		AvatarURL: profile.AvatarURL,
		Bio:       profile.Bio,
	}, nil
}

// UpdateMe 部分更新当前用户资料，nil 字段保持原值。
func (s *Service) UpdateMe(ctx context.Context, userID int64, nickname, avatarURL, bio *string) (*Profile, error) {
	profile, err := s.repo.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := profile.Update(nickname, avatarURL, bio); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateProfile(ctx, profile); err != nil {
		return nil, err
	}
	return &Profile{
		UserID:    profile.UserID,
		Nickname:  profile.Nickname,
		AvatarURL: profile.AvatarURL,
		Bio:       profile.Bio,
	}, nil
}

// Logout 无状态登出：JWT 服务端不维护会话，直接视为成功，由传输层返回 204。
func (s *Service) Logout(_ context.Context, _ int64) error {
	return nil
}
