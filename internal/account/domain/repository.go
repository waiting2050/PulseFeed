package domain

import "context"

type Repository interface {
	Create(ctx context.Context, user *User) error
	FindByAccount(ctx context.Context, account string) (*User, error)
	FindByID(ctx context.Context, id int64) (*User, error)
	UpdateProfile(ctx context.Context, id int64, nickname, avatarURL, bio string) error
}
