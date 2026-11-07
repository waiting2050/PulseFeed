// errors.go 集中定义互动领域错误哨兵，传输层据此映射 HTTP 状态码。
package dominteraction

import "errors"

var (
	// ErrInvalidActionType 表示行为类型不在 like|save 集合内。
	ErrInvalidActionType = errors.New("invalid action type")
	// ErrInvalidCommentContent 表示评论内容为空或超过长度上限。
	ErrInvalidCommentContent = errors.New("invalid comment content")
	// ErrClipNotFound 表示目标 clip 不存在或已不可互动（非发布态）。
	ErrClipNotFound = errors.New("clip not found")
	// ErrCommentNotFound 表示评论不存在。
	ErrCommentNotFound = errors.New("comment not found")
	// ErrCommentPermissionDenied 表示操作者既非评论作者也非 clip 创作者。
	ErrCommentPermissionDenied = errors.New("comment permission denied")
	// ErrInvalidCursor 表示分页游标无法解析。
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrInvalidLimit 表示分页条数越界。
	ErrInvalidLimit = errors.New("invalid limit")
)
