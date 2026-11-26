// entity.go 定义上传会话领域实体：会话状态机、分片元信息与状态命名。
package domupload

import "time"

// Status 是上传会话状态；数值与 pf_upload_sessions.status 列一一对应。
type Status int8

const (
	// StatusPending 表示会话可继续接收分片。
	StatusPending Status = 0
	// StatusMerging 表示已有 complete 抢到合并权，不再接收分片。
	StatusMerging Status = 1
	// StatusCompleted 表示合并、校验与落位均已完成。
	StatusCompleted Status = 2
	// StatusCanceled 表示用户主动取消。
	StatusCanceled Status = 3
	// StatusFailed 表示校验或合并失败。
	StatusFailed Status = 4
	// StatusExpired 表示超过 24h 未推进，按 TTL 回收。
	StatusExpired Status = 5
)

// String 返回对外展示的状态名，与响应字段 status 的取值一致。
func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusMerging:
		return "merging"
	case StatusCompleted:
		return "completed"
	case StatusCanceled:
		return "canceled"
	case StatusFailed:
		return "failed"
	case StatusExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// Terminal 判断状态是否为终态；终态会话不再接受分片与合并。
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusCanceled || s == StatusFailed || s == StatusExpired
}

// Session 是一次分片上传的会话元数据；以 MySQL 行为索引，分片文件为落盘事实。
type Session struct {
	UploadID     string
	UserID       int64
	Kind         string
	Filename     string
	SizeBytes    int64
	ChunkSize    int64
	TotalParts   int
	SHA256       string
	Status       Status
	URL          string
	CoverURL     string
	DurationMS   int64
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Part 是一条已上传分片的元数据。
type Part struct {
	UploadID   string
	PartNumber int
	SizeBytes  int64
	CreatedAt  time.Time
}
