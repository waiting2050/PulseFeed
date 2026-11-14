// cursor_codec.go 为翻页游标叠加服务端 HMAC 签名，收敛“任意伪造游标 → 全新页缓存键”的可构造空间。
package domfeed

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

// cursorSignatureBytes 是签名截断后的字节数：128 bit 足以抵抗离线伪造，
// 同时把游标长度控制在一行 URL 参数的量级。
const cursorSignatureBytes = 16

// CursorCodec 为游标编码叠加服务端签名，输出格式为 "<payload>.<sig>"：
// payload 仍是 EncodeCursor 产出的 base64url(JSON)，sig 是 HMAC-SHA256(payload) 的截断值。
// 客户端只需原样回传，游标对前端依然不透明，语义、排序与去重规则完全不变。
type CursorCodec struct {
	secret []byte
}

// NewCursorCodec 使用给定密钥创建游标编解码器；密钥长度由启动校验保证。
func NewCursorCodec(secret string) *CursorCodec {
	return &CursorCodec{secret: []byte(secret)}
}

// Encode 编码并签名游标；nil 游标返回空串（表示首屏）。
func (c *CursorCodec) Encode(cursor *Cursor) string {
	payload := EncodeCursor(cursor)
	if payload == "" {
		return ""
	}
	return payload + "." + c.sign(payload)
}

// Decode 校验签名并解析游标。
// legacy=true 表示收到签名上线前的旧游标（不含 "." 分隔符）：调用方应降级为首屏并记日志，
// 而不是报错，保证旧客户端不会因为游标升级拿到 500。
// 签名不匹配返回 ErrInvalidCursorSignature，内容非法返回 ErrInvalidCursor，均映射为 400。
func (c *CursorCodec) Decode(token string) (*Cursor, bool, error) {
	if token == "" {
		return nil, false, nil
	}
	// base64url 字母表不含 "."，因此分隔符只可能来自签名格式。
	payload, sig, signed := strings.Cut(token, ".")
	if !signed {
		cursor, err := DecodeCursor(token)
		if err != nil {
			return nil, false, err
		}
		return cursor, true, nil
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(c.sign(payload))) != 1 {
		return nil, false, ErrInvalidCursorSignature
	}
	cursor, err := DecodeCursor(payload)
	if err != nil {
		return nil, false, err
	}
	return cursor, false, nil
}

// sign 计算 payload 的截断 HMAC-SHA256 签名，并以 base64url 编码。
func (c *CursorCodec) sign(payload string) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(payload))
	sum := mac.Sum(nil)[:cursorSignatureBytes]
	return base64.RawURLEncoding.EncodeToString(sum)
}
