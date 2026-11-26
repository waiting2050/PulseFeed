// errors.go 集中定义 feed 领域错误哨兵，传输层据此映射 HTTP 状态码。
package domfeed

import (
	"errors"
	"time"
)

var (
	// ErrUnsupportedScene 表示请求场景不在已实现的场景集合内。
	ErrUnsupportedScene = errors.New("unsupported scene")
	// ErrInvalidCursor 表示分页游标无法解析。
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrInvalidCursorSignature 表示游标签名校验失败（被篡改或伪造），映射为 400。
	ErrInvalidCursorSignature = errors.New("invalid cursor signature")
	// ErrInvalidLimit 表示分页条数越界。
	ErrInvalidLimit = errors.New("invalid limit")
	// ErrViewerRequired 表示该场景要求登录读者（following 等场景预留，S11 接入）。
	ErrViewerRequired = errors.New("viewer required")
)

// ErrFeedRateLimited 表示 following/hot 的请求频率超过配额，映射为 429；
// RetryAfter 是限流窗口的剩余时长，传输层用它填充 Retry-After 响应头。
type ErrFeedRateLimited struct {
	RetryAfter time.Duration
}

// Error 实现 error 接口。
func (e *ErrFeedRateLimited) Error() string { return "feed rate limited" }
