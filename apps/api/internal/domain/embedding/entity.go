// entity.go 定义文本向量的领域类型、固定维度与余弦相似度计算。
package domembedding

import "math"

// Dimension 是 clip 文本向量的固定维度，hash n-gram 与持久化列都按该值对齐。
const Dimension = 256

// Vector 是一段文本的稠密向量表示，长度恒为 Dimension，落库前已做 L2 归一化。
type Vector []float64

// NewVector 校验维度并拷贝入参，返回可安全共享的向量副本；维度不符返回 ErrInvalidDimension。
func NewVector(values []float64) (Vector, error) {
	if len(values) != Dimension {
		return nil, ErrInvalidDimension
	}
	clone := make(Vector, Dimension)
	copy(clone, values)
	return clone, nil
}

// Cosine 计算两个向量的余弦相似度，取值 [-1,1]；维度不一致或任一为零向量时返回 0。
func (v Vector) Cosine(other Vector) float64 {
	if len(v) == 0 || len(v) != len(other) {
		return 0
	}
	var dot, normV, normOther float64
	for i := range v {
		dot += v[i] * other[i]
		normV += v[i] * v[i]
		normOther += other[i] * other[i]
	}
	if normV == 0 || normOther == 0 {
		return 0
	}
	return dot / (math.Sqrt(normV) * math.Sqrt(normOther))
}

// Normalized 返回 L2 归一化后的向量副本；零向量原样返回零值副本，避免除零。
func (v Vector) Normalized() Vector {
	clone := make(Vector, len(v))
	copy(clone, v)

	var norm float64
	for _, value := range clone {
		norm += value * value
	}
	if norm == 0 {
		return clone
	}
	norm = math.Sqrt(norm)
	for i := range clone {
		clone[i] /= norm
	}
	return clone
}

// IsZero 报告向量是否为空或全部为 0（无文本特征）。
func (v Vector) IsZero() bool {
	for _, value := range v {
		if value != 0 {
			return false
		}
	}
	return true
}
