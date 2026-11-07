// errors.go 集中定义 feed 领域错误哨兵，传输层据此映射 HTTP 状态码。
package domfeed

import "errors"

var (
	// ErrUnsupportedScene 表示请求场景不在已实现的场景集合内。
	ErrUnsupportedScene = errors.New("unsupported scene")
	// ErrInvalidCursor 表示分页游标无法解析。
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrInvalidLimit 表示分页条数越界。
	ErrInvalidLimit = errors.New("invalid limit")
	// ErrViewerRequired 表示该场景要求登录读者（following 等场景预留，S11 接入）。
	ErrViewerRequired = errors.New("viewer required")
)
