// errors.go 集中定义 relation 领域错误哨兵，传输层据此映射 HTTP 状态码。
package domrelation

import "errors"

var (
	// ErrFollowSelf 表示关注目标是当前用户自己。
	ErrFollowSelf = errors.New("follow self forbidden")
	// ErrTargetNotFound 表示目标用户不存在或状态异常。
	ErrTargetNotFound = errors.New("target user not found")
	// ErrInvalidCursor 表示分页游标无法解析。
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrInvalidLimit 表示分页条数越界。
	ErrInvalidLimit = errors.New("invalid limit")
)
