package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"pulsefeed/internal/account/domain"
	"pulsefeed/internal/shared/jwtx"
)

var accountPattern = regexp.MustCompile(`^[a-zA-Z0-9_]{4,32}$`)

type RegisterInput struct {
	Account  string
	Password string
	Nickname string
}

type ProfileUpdate struct {
	Nickname  *string
	AvatarURL *string
	Bio       *string
}

type LoginResult struct {
	Token     string
	ExpiresIn int64
	User      *domain.User
}

type Service struct {
	repo domain.Repository
	jwt  *jwtx.Manager
}

func NewService(repo domain.Repository, jwt *jwtx.Manager) *Service {
	return &Service{repo: repo, jwt: jwt}
}

func (s *Service) Register(ctx context.Context, input RegisterInput) (*domain.User, error) {
	account := strings.ToLower(strings.TrimSpace(input.Account))
	if !accountPattern.MatchString(account) {
		return nil, domain.ErrInvalidAccount
	}
	if len(input.Password) < 8 || len(input.Password) > 64 {
		return nil, domain.ErrInvalidPassword
	}

	nickname := strings.TrimSpace(input.Nickname)
	if nickname == "" {
		nickname = account
	}
	if len([]rune(nickname)) > 32 {
		return nil, domain.ErrInvalidNickname
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	user := &domain.User{
		Account:      account,
		PasswordHash: string(hash),
		Nickname:     nickname,
		Role:         domain.RoleUser,
		Status:       domain.StatusOK,
	}
	if err := s.repo.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) Login(ctx context.Context, account, password string) (*LoginResult, error) {
	user, err := s.repo.FindByAccount(ctx, strings.ToLower(strings.TrimSpace(account)))
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, err
	}
	if !user.IsActive() {
		return nil, domain.ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	token, err := s.jwt.Issue(user.ID)
	if err != nil {
		return nil, err
	}
	return &LoginResult{
		Token:     token,
		ExpiresIn: int64(s.jwt.TTL().Seconds()),
		User:      user,
	}, nil
}

func (s *Service) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	if id <= 0 {
		return nil, domain.ErrUserNotFound
	}
	return s.repo.FindByID(ctx, id)
}

func (s *Service) UpdateProfile(ctx context.Context, userID int64, update ProfileUpdate) (*domain.User, error) {
	current, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	nickname := current.Nickname
	avatarURL := current.AvatarURL
	bio := current.Bio

	if update.Nickname != nil {
		nickname = strings.TrimSpace(*update.Nickname)
		length := len([]rune(nickname))
		if length == 0 || length > 32 {
			return nil, domain.ErrInvalidNickname
		}
	}
	if update.AvatarURL != nil {
		avatarURL = strings.TrimSpace(*update.AvatarURL)
		if len(avatarURL) > 255 {
			return nil, domain.ErrInvalidProfile
		}
	}
	if update.Bio != nil {
		bio = strings.TrimSpace(*update.Bio)
		if len([]rune(bio)) > 255 {
			return nil, domain.ErrInvalidProfile
		}
	}

	if err := s.repo.UpdateProfile(ctx, userID, nickname, avatarURL, bio); err != nil {
		return nil, err
	}
	return s.repo.FindByID(ctx, userID)
}
