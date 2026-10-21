package domain

import "context"

// Repository 保存关注关系，重复关注与重复取关都按幂等处理。
type Repository interface {
	UserExists(ctx context.Context, userID int64) (bool, error)
	Follow(ctx context.Context, followerID, followeeID int64) (bool, error)
	Unfollow(ctx context.Context, followerID, followeeID int64) (bool, error)
	ListFolloweeIDs(ctx context.Context, followerID int64, limit int) ([]int64, error)
}
