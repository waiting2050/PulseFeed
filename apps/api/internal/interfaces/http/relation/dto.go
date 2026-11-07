// dto.go 定义 relation 模块的传输层请求与响应结构。
package httprelation

import "time"

// followResponse 是关注/取关的统一响应：回执含双方计数与当前关系状态。
type followResponse struct {
	UserID         int64 `json:"user_id"`
	TargetUserID   int64 `json:"target_user_id"`
	Status         int   `json:"status"`
	Following      bool  `json:"following"`
	FollowingCount int64 `json:"following_count"`
	FollowerCount  int64 `json:"follower_count"`
}

// relationStatsResponse 是用户关系概览响应：计数与当前读者关注状态。
type relationStatsResponse struct {
	UserID         int64 `json:"user_id"`
	FollowingCount int64 `json:"following_count"`
	FollowerCount  int64 `json:"follower_count"`
	Following      bool  `json:"following"`
	IsSelf         bool  `json:"is_self"`
}

// relationEntryResponse 是关注/粉丝列表的条目。
type relationEntryResponse struct {
	UserID     int64     `json:"user_id"`
	Nickname   string    `json:"nickname"`
	AvatarURL  string    `json:"avatar_url"`
	Bio        string    `json:"bio"`
	FollowedAt time.Time `json:"followed_at"`
}

// relationListResponse 是关注/粉丝列表的分页响应。
type relationListResponse struct {
	Items      []relationEntryResponse `json:"items"`
	NextCursor string                  `json:"next_cursor"`
	HasMore    bool                    `json:"has_more"`
}
