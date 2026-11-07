// entity.go 定义 relation 领域的关注关系实体、状态常量、关注回执与列表游标编解码。
package domrelation

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

const (
	// StatusFollowing 表示关注关系有效（关注中）。
	StatusFollowing = 1
	// StatusUnfollowed 表示关注关系已取消；历史行保留用于审计。
	StatusUnfollowed = 2

	// MaxLimit 是关注/粉丝列表单页条数上限。
	MaxLimit = 100
	// DefaultLimit 是未显式指定 limit 时的默认页大小。
	DefaultLimit = 20
)

// Follow 是一条关注关系记录；同一 (UserID, TargetUserID) 只有一行，取关为状态置位。
type Follow struct {
	ID             int64
	UserID         int64
	TargetUserID   int64
	Status         int
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewFollow 校验并创建关注关系：不允许关注自己，初始状态为关注中。
func NewFollow(userID, targetUserID int64) (*Follow, error) {
	if userID <= 0 || targetUserID <= 0 {
		return nil, ErrTargetNotFound
	}
	if userID == targetUserID {
		return nil, ErrFollowSelf
	}
	return &Follow{
		UserID:       userID,
		TargetUserID: targetUserID,
		Status:       StatusFollowing,
	}, nil
}

// FollowResult 是关注/取关后的回执：FollowingCount 为当前用户的关注数，
// FollowerCount 为目标用户的粉丝数；Changed 表示本次调用是否发生了状态迁移（供回填/消息触发）。
type FollowResult struct {
	UserID         int64
	TargetUserID   int64
	Status         int
	Following      bool
	FollowingCount int64
	FollowerCount  int64
	Changed        bool
}

// ListEntry 是关注/粉丝列表的条目：对方用户 ID、展示资料与关系最近更新时间。
type ListEntry struct {
	UserID     int64
	Nickname   string
	AvatarURL  string
	Bio        string
	FollowedAt time.Time
}

// ListCursor 是关注/粉丝列表按 (updated_at, peer_user_id) 严格递减翻页的游标。
type ListCursor struct {
	FollowedAt time.Time `json:"f"`
	UserID     int64     `json:"u"`
}

// EncodeListCursor 将列表游标编码为 URL 安全字符串；nil 游标编码为空串。
func EncodeListCursor(c *ListCursor) string {
	if c == nil {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeListCursor 解析列表游标；空串返回 nil 表示首页，非法串返回 ErrInvalidCursor。
func DecodeListCursor(s string) (*ListCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	cursor := &ListCursor{}
	if err := json.Unmarshal(raw, cursor); err != nil {
		return nil, ErrInvalidCursor
	}
	return cursor, nil
}
