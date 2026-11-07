// entity.go 定义 clip 领域的 Clip 实体、发布/删除状态机与列表游标。
package domclip

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// StatusPublished 表示 clip 已发布、对读者可见。
	StatusPublished = 1
	// StatusDeleted 表示 clip 已被创作者删除，读取路径视为不存在。
	StatusDeleted = 2

	// 标题长度上限（按字符/rune 计，去空白后判定；列宽 128 字符）。
	titleMaxLen = 128

	// MaxListLimit 是创作者列表单页条数上限。
	MaxListLimit = 50
	// DefaultListLimit 是未显式指定 limit 时的默认页大小。
	DefaultListLimit = 20
)

// Clip 是内容聚合根，记录创作者、标题与媒体地址；发布即可见，删除为状态置位。
// DurationMS 是媒体时长（毫秒），探测不可用时为 0，读取侧按“无时长”处理。
type Clip struct {
	ID             int64
	CreatorID      int64
	Title          string
	VideoURL       string
	CoverURL       string
	DurationMS     int64
	Status         int
	IdempotencyKey string
	PublishedAt    time.Time
}

// NewClip 创建并发布一条 clip：标题去空白非空且限长，视频地址与幂等键非空；
// 时长由上传探测提供，负值按 0 处理，避免脏数据进入展示层。
func NewClip(creatorID int64, title, videoURL, coverURL string, durationMS int64, idempotencyKey string) (*Clip, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > titleMaxLen {
		return nil, ErrInvalidTitle
	}
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return nil, ErrInvalidVideoURL
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, ErrInvalidIdempotencyKey
	}
	if durationMS < 0 {
		durationMS = 0
	}
	return &Clip{
		CreatorID:      creatorID,
		Title:          title,
		VideoURL:       videoURL,
		CoverURL:       strings.TrimSpace(coverURL),
		DurationMS:     durationMS,
		Status:         StatusPublished,
		IdempotencyKey: idempotencyKey,
		PublishedAt:    time.Now(),
	}, nil
}

// RestoreClip 从持久化记录恢复实体，读取路径不再执行发布校验。
func RestoreClip(id, creatorID int64, title, videoURL, coverURL string, durationMS int64, status int, idempotencyKey string, publishedAt time.Time) *Clip {
	return &Clip{
		ID:             id,
		CreatorID:      creatorID,
		Title:          title,
		VideoURL:       videoURL,
		CoverURL:       coverURL,
		DurationMS:     durationMS,
		Status:         status,
		IdempotencyKey: idempotencyKey,
		PublishedAt:    publishedAt,
	}
}

// Delete 将 clip 置为已删除状态，重复调用幂等。
func (c *Clip) Delete() {
	c.Status = StatusDeleted
}

// Visible 判断 clip 是否对读者可见。
func (c *Clip) Visible() bool {
	return c.Status == StatusPublished
}

// Cursor 是按 (published_at, clip_id) 复合键严格递减翻页的游标。
type Cursor struct {
	PublishedAt time.Time `json:"p"`
	ClipID      int64     `json:"c"`
}

// EncodeCursor 将游标编码为 URL 安全字符串；nil 游标编码为空串。
func EncodeCursor(c *Cursor) string {
	if c == nil {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor 解析游标字符串；空串返回 nil 表示首页，非法串返回 ErrInvalidCursor。
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	cursor := &Cursor{}
	if err := json.Unmarshal(raw, cursor); err != nil {
		return nil, ErrInvalidCursor
	}
	return cursor, nil
}
