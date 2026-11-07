// errors.go 定义 embedding 领域的哨兵错误，供上层映射 HTTP 状态与重试策略。
package domembedding

import "errors"

var (
	// ErrInvalidDimension 表示向量维度与 Dimension 不一致。
	ErrInvalidDimension = errors.New("向量维度非法")
	// ErrClipNotFound 表示待生成向量的 clip 不存在或不可见（已发布态才允许生成）。
	ErrClipNotFound = errors.New("clip 不存在或不可见")
)
