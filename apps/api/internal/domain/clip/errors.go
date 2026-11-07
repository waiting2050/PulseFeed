// errors.go 集中定义 clip 领域错误哨兵，传输层据此映射 HTTP 状态码。
package domclip

import "errors"

var (
	// ErrInvalidTitle 表示标题为空或超过长度上限（128 个字符）。
	ErrInvalidTitle = errors.New("标题不能为空且不能超过 128 个字符")
	// ErrInvalidVideoURL 表示视频地址为空。
	ErrInvalidVideoURL = errors.New("invalid video url")
	// ErrInvalidIdempotencyKey 表示幂等键为空。
	ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")
	// ErrClipNotFound 表示 clip 不存在或已删除。
	ErrClipNotFound = errors.New("clip not found")
	// ErrClipExists 表示 (creator_id, idempotency_key) 唯一索引冲突，用于并发兜底。
	ErrClipExists = errors.New("clip already exists")
	// ErrNotClipOwner 表示操作者不是 clip 创作者。
	ErrNotClipOwner = errors.New("not clip owner")
	// ErrInvalidCursor 表示分页游标无法解析。
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrInvalidLimit 表示分页条数越界。
	ErrInvalidLimit = errors.New("invalid limit")
)
