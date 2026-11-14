// cursor_codec_test.go 覆盖游标签名往返、篡改拒绝与无签名旧游标降级识别。
package domfeed_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	domfeed "pulsefeed/internal/domain/feed"
)

// TestCursorCodecRoundTrip 验证签名游标能原样还原语义字段（含 hot/for-you 的分数与快照）。
func TestCursorCodecRoundTrip(t *testing.T) {
	codec := domfeed.NewCursorCodec(strings.Repeat("k", 32))
	snapshot := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	original := &domfeed.Cursor{
		PublishedAt: time.Date(2026, 3, 1, 11, 30, 0, 123000000, time.UTC),
		ClipID:      42,
		Score:       98.5,
		SnapshotAt:  &snapshot,
		Version:     domfeed.CursorVersionMilli,
	}

	token := codec.Encode(original)
	if token == "" || !strings.Contains(token, ".") {
		t.Fatalf("签名游标应包含分隔符，实际 %q", token)
	}
	decoded, legacy, err := codec.Decode(token)
	if err != nil || legacy {
		t.Fatalf("签名游标解析失败: legacy=%v err=%v", legacy, err)
	}
	if decoded.ClipID != original.ClipID || decoded.Version != original.Version ||
		!decoded.PublishedAt.Equal(original.PublishedAt) || decoded.Score != original.Score {
		t.Fatalf("游标语义不一致: %+v", decoded)
	}
	if decoded.SnapshotAt == nil || !decoded.SnapshotAt.Equal(snapshot) {
		t.Fatalf("for-you 快照时间丢失: %+v", decoded.SnapshotAt)
	}
}

// TestCursorCodecRejectsTamperedSignature 验证篡改载荷或签名都会被拒绝。
func TestCursorCodecRejectsTamperedSignature(t *testing.T) {
	codec := domfeed.NewCursorCodec(strings.Repeat("k", 32))
	token := codec.Encode(&domfeed.Cursor{ClipID: 42, Version: domfeed.CursorVersionMilli})

	// 篡改签名（保持原载荷）。
	tamperedSig := token[:len(token)-1] + "A"
	if tamperedSig == token {
		tamperedSig = token[:len(token)-1] + "B"
	}
	if _, _, err := codec.Decode(tamperedSig); !errors.Is(err, domfeed.ErrInvalidCursorSignature) {
		t.Fatalf("签名错误应返回 ErrInvalidCursorSignature，实际 %v", err)
	}

	// 篡改载荷（保持原签名）：重建另一个 clip_id 的载荷。
	_, sig, _ := strings.Cut(token, ".")
	other := codec.Encode(&domfeed.Cursor{ClipID: 43, Version: domfeed.CursorVersionMilli})
	otherPayload, _, _ := strings.Cut(other, ".")
	if _, _, err := codec.Decode(otherPayload + "." + sig); !errors.Is(err, domfeed.ErrInvalidCursorSignature) {
		t.Fatalf("载荷被篡改应返回 ErrInvalidCursorSignature，实际 %v", err)
	}
}

// TestCursorCodecLegacyUnsignedCursor 验证无签名旧游标可解析但被标记为 legacy，交由调用方降级首屏。
func TestCursorCodecLegacyUnsignedCursor(t *testing.T) {
	codec := domfeed.NewCursorCodec(strings.Repeat("k", 32))
	legacyToken := domfeed.EncodeCursor(&domfeed.Cursor{ClipID: 7, Version: domfeed.CursorVersionMilli})

	cursor, legacy, err := codec.Decode(legacyToken)
	if err != nil {
		t.Fatalf("旧游标不应报错: %v", err)
	}
	if !legacy || cursor == nil || cursor.ClipID != 7 {
		t.Fatalf("旧游标应标记 legacy 且可解析: legacy=%v cursor=%+v", legacy, cursor)
	}
}

// TestCursorCodecInvalidAndEmpty 验证空串表示首屏、非法串返回 ErrInvalidCursor。
func TestCursorCodecInvalidAndEmpty(t *testing.T) {
	codec := domfeed.NewCursorCodec(strings.Repeat("k", 32))

	cursor, legacy, err := codec.Decode("")
	if cursor != nil || legacy || err != nil {
		t.Fatalf("空游标应返回 nil/false/nil: %v %v %v", cursor, legacy, err)
	}
	if _, _, err := codec.Decode("%%%"); !errors.Is(err, domfeed.ErrInvalidCursor) {
		t.Fatalf("非法旧格式应返回 ErrInvalidCursor，实际 %v", err)
	}
	if _, _, err := codec.Decode("abc.def"); !errors.Is(err, domfeed.ErrInvalidCursorSignature) {
		t.Fatalf("签名不匹配应返回 ErrInvalidCursorSignature，实际 %v", err)
	}
}
