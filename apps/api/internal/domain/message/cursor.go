// cursor.go 实现 ListCursor 的 base64 RawURL JSON 编码/解码，供接口层透传。
package dommessage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// EncodeListCursor 把游标编码为 URL 友好的 base64 字符串。
func EncodeListCursor(c *ListCursor) string {
	if c == nil || c.CreatedAt.IsZero() || c.ID <= 0 {
		return ""
	}
	b, _ := json.Marshal(struct {
		T time.Time `json:"t"`
		I int64     `json:"i"`
	}{T: c.CreatedAt, I: c.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeListCursor 把接口层传入的游标字符串解码为领域游标；空串返回 nil 表示首页。
func DecodeListCursor(raw string) (*ListCursor, error) {
	if raw == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCursorInvalid, err)
	}
	var payload struct {
		T time.Time `json:"t"`
		I int64     `json:"i"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCursorInvalid, err)
	}
	if payload.T.IsZero() || payload.I <= 0 {
		return nil, ErrCursorInvalid
	}
	return &ListCursor{CreatedAt: payload.T, ID: payload.I}, nil
}
