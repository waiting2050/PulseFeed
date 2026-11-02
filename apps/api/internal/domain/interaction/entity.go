// entity.go 定义互动领域的行为类型、行为/评论实体、状态常量与评论游标编解码。
package dominteraction

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// StatusActive 表示行为记录有效（点赞/收藏处于启用态）。
	StatusActive = 1
	// StatusCanceled 表示行为记录已取消。
	StatusCanceled = 2

	// CommentStatusVisible 表示评论正常展示。
	CommentStatusVisible = 1
	// CommentStatusDeleted 表示评论已删除。
	CommentStatusDeleted = 2

	// commentMaxRunes 是评论内容长度上限（按字符计）。
	commentMaxRunes = 1000

	// MaxListLimit 是评论列表单页条数上限。
	MaxListLimit = 100
	// DefaultListLimit 是未显式指定 limit 时的默认页大小。
	DefaultListLimit = 20
)

// ActionType 是互动行为类型，点赞与收藏共享同一套状态机。
type ActionType string

const (
	// ActionLike 表示点赞行为。
	ActionLike ActionType = "LIKE"
	// ActionSave 表示收藏行为（词汇映射 favorite→save）。
	ActionSave ActionType = "SAVE"
)

// Valid 报告行为类型是否为已支持的取值。
func (t ActionType) Valid() bool {
	return t == ActionLike || t == ActionSave
}

// Action 是一次点赞/收藏行为的状态记录；同一 (UserID, ClipID, Type) 只有一行。
type Action struct {
	UserID         int64
	ClipID         int64
	Type           ActionType
	Status         int
	IdempotencyKey string
}

// ActionResult 是行为状态变更后的回执，携带该 clip 的最新计数；
// Changed 标记本次调用是否真正发生了状态翻转，供热度桶只在计数变化时累加。
type ActionResult struct {
	ClipID     int64
	ActionType ActionType
	Active     bool
	Changed    bool
	LikeCount  int64
	SaveCount  int64
}

// Comment 是一条评论，展示字段（昵称/头像）来自资料表联查。
type Comment struct {
	ID        int64
	ClipID    int64
	UserID    int64
	Nickname  string
	AvatarURL string
	Content   string
	Status    int
	CreatedAt time.Time
}

// NewComment 校验并创建一条评论：内容去首尾空白后长度需在 1..1000 字符之间。
func NewComment(clipID, userID int64, content string) (*Comment, error) {
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > commentMaxRunes {
		return nil, ErrInvalidCommentContent
	}
	return &Comment{
		ClipID:  clipID,
		UserID:  userID,
		Content: content,
		Status:  CommentStatusVisible,
	}, nil
}

// DeletedComment 是删除评论的回执。
type DeletedComment struct {
	CommentID    int64
	Status       int
	CommentCount int64
}

// CommentCursor 是评论列表按 (created_at, id) 严格递减翻页的游标。
type CommentCursor struct {
	CreatedAt time.Time `json:"t"`
	CommentID int64     `json:"i"`
}

// EncodeCommentCursor 将评论游标编码为 URL 安全字符串；nil 游标编码为空串。
func EncodeCommentCursor(c *CommentCursor) string {
	if c == nil {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCommentCursor 解析评论游标；空串返回 nil 表示首页，非法串返回 ErrInvalidCursor。
func DecodeCommentCursor(s string) (*CommentCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	cursor := &CommentCursor{}
	if err := json.Unmarshal(raw, cursor); err != nil {
		return nil, ErrInvalidCursor
	}
	return cursor, nil
}
