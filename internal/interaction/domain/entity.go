package domain

import (
	"errors"
	"time"
)

const (
	ActionLike     = 1
	ActionFavorite = 2
)

// 互动行为折算成热榜分数的权重，收藏和评论比点赞更能代表有效互动。
const (
	HotScoreLike     = 1
	HotScoreFavorite = 2
	HotScoreComment  = 3
)

var (
	ErrVideoNotFound   = errors.New("视频不存在")
	ErrCommentNotFound = errors.New("评论不存在")
	ErrForbidden       = errors.New("无权删除该评论")
	ErrInvalidComment  = errors.New("评论内容需为 1-500 个字符")
	ErrInvalidCursor   = errors.New("分页游标不合法")
	ErrInvalidAction   = errors.New("不支持的互动类型")
)

type Comment struct {
	ID        int64
	VideoID   int64
	UserID    int64
	Content   string
	CreatedAt time.Time
}
