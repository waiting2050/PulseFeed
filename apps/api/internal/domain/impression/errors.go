// errors.go 集中定义曝光领域错误哨兵，传输层据此映射 HTTP 状态码。
package domimpression

import "errors"

var (
	// ErrInvalidEventID 表示播放事件 ID（幂等键）为空或超过长度上限。
	ErrInvalidEventID = errors.New("invalid event id")
	// ErrInvalidPlayDuration 表示观看时长为负或超过单日上限。
	ErrInvalidPlayDuration = errors.New("invalid play duration")
	// ErrClipNotFound 表示目标 clip 不存在或已不可播放（非发布态）。
	ErrClipNotFound = errors.New("clip not found")
)
