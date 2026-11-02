// errors.go 定义 recommendation 领域的哨兵错误，供 feed 与 internal 接口映射状态码。
package domrecommendation

import "errors"

var (
	// ErrViewerRequired 表示推荐需要登录读者身份。
	ErrViewerRequired = errors.New("推荐场景需要登录")
	// ErrInvalidCursor 表示推荐游标缺失 snapshot_at 或无法解析。
	ErrInvalidCursor = errors.New("非法的推荐游标")
	// ErrInvalidLimit 表示召回/查询条数越界或非正。
	ErrInvalidLimit = errors.New("非法的条数")
	// ErrImpressionNotFound 表示读者对该 clip 没有曝光记录。
	ErrImpressionNotFound = errors.New("曝光记录不存在")
)
