package domain

import (
	"errors"
	"time"
)

const (
	StatusPublished = 1
	StatusDeleted   = 2
)

var (
	ErrVideoNotFound       = errors.New("视频不存在")
	ErrInvalidTitle        = errors.New("视频标题需为 1-100 个字符")
	ErrInvalidMediaURL     = errors.New("视频地址不合法")
	ErrInvalidCoverURL     = errors.New("封面地址不合法")
	ErrInvalidCursor       = errors.New("分页游标不合法")
	ErrInvalidIdempotency  = errors.New("幂等键长度不能超过 64")
	ErrIdempotencyConflict = errors.New("幂等键冲突")
)

type Video struct {
	ID             int64
	AuthorID       int64
	Title          string
	MediaURL       string
	CoverURL       string
	Status         int
	IdempotencyKey string
	PublishedAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Stat struct {
	VideoID       int64
	LikeCount     int64
	CommentCount  int64
	FavoriteCount int64
}

func (v *Video) IsPublished() bool {
	return v.Status == StatusPublished
}
