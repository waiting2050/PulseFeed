package domain

import "errors"

var (
	ErrUserNotFound     = errors.New("用户不存在")
	ErrCannotFollowSelf = errors.New("不能关注自己")
)

type Relation struct {
	ID         int64
	FollowerID int64
	FolloweeID int64
}
