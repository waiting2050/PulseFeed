// model.go 定义 embedding 模块 pf_clip_embeddings 表的 GORM 模型。
package infraembedding

import "time"

// ClipEmbeddingModel 映射 pf_clip_embeddings 表：主键即 clip ID，向量按 JSON 数组文本存储。
type ClipEmbeddingModel struct {
	ClipID    int64     `gorm:"column:clip_id;primaryKey"`
	Dim       int       `gorm:"column:dim;not null"`
	Vector    string    `gorm:"column:vector;type:text;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 指定向量表名。
func (ClipEmbeddingModel) TableName() string {
	return "pf_clip_embeddings"
}
