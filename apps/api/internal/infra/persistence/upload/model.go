// model.go 定义分片上传的两张表：pf_upload_sessions 与 pf_upload_parts。
package infraupload

import "time"

// SessionModel 映射 pf_upload_sessions；upload_id 唯一索引承担对外标识的幂等。
// status 数值语义见 domupload.Status；sha256 只存客户端期望值，不存服务端计算结果。
type SessionModel struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement"`
	UploadID     string    `gorm:"column:upload_id;size:32;not null;uniqueIndex:uk_upload_id"`
	UserID       int64     `gorm:"column:user_id;not null;index:idx_user_status,priority:1"`
	Kind         string    `gorm:"column:kind;size:16;not null"`
	Filename     string    `gorm:"column:filename;size:255;not null"`
	SizeBytes    int64     `gorm:"column:size_bytes;not null"`
	ChunkSize    int       `gorm:"column:chunk_size;not null"`
	TotalParts   int       `gorm:"column:total_parts;not null"`
	SHA256       *string   `gorm:"column:sha256;type:char(64);null"`
	Status       int8      `gorm:"column:status;not null;default:0;index:idx_user_status,priority:2;index:idx_status_updated,priority:1"`
	URL          string    `gorm:"column:url;size:512;not null;default:''"`
	CoverURL     string    `gorm:"column:cover_url;size:512;not null;default:''"`
	DurationMS   int64     `gorm:"column:duration_ms;not null;default:0"`
	ErrorMessage string    `gorm:"column:error_message;size:255;not null;default:''"`
	CreatedAt    time.Time `gorm:"column:created_at;type:datetime(3);not null"`
	UpdatedAt    time.Time `gorm:"column:updated_at;type:datetime(3);not null;index:idx_status_updated,priority:2"`
}

// TableName 指定上传会话表名。
func (SessionModel) TableName() string {
	return "pf_upload_sessions"
}

// PartModel 映射 pf_upload_parts；(upload_id, part_number) 唯一索引让重复分片天然幂等。
type PartModel struct {
	ID         int64     `gorm:"column:id;primaryKey;autoIncrement"`
	UploadID   string    `gorm:"column:upload_id;size:32;not null;uniqueIndex:uk_upload_part,priority:1"`
	PartNumber int       `gorm:"column:part_number;type:int;not null;uniqueIndex:uk_upload_part,priority:2"`
	SizeBytes  int       `gorm:"column:size_bytes;type:int;not null"`
	CreatedAt  time.Time `gorm:"column:created_at;type:datetime(3);not null"`
}

// TableName 指定分片元数据表名。
func (PartModel) TableName() string {
	return "pf_upload_parts"
}
