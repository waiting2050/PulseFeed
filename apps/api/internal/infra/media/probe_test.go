// probe_test.go 覆盖时长解析与 ffmpeg/ffprobe 缺失时的降级路径。
package media

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestParseDurationMS 验证秒数文本换算毫秒，含空白、四舍五入与非法输入。
func TestParseDurationMS(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "整数秒", raw: "3\n", want: 3000},
		{name: "小数秒", raw: "12.3456", want: 12346},
		{name: "零时长", raw: " 0 ", want: 0},
		{name: "亚秒", raw: "0.4", want: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDurationMS(tc.raw)
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if got != tc.want {
				t.Fatalf("期望 %d ms，实际 %d ms", tc.want, got)
			}
		})
	}
}

// TestParseDurationMSRejectsInvalid 验证空值、N/A、负数与非数字均返回错误。
func TestParseDurationMSRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"", "  \n", "N/A", "-1.5", "abc"} {
		if _, err := ParseDurationMS(raw); err == nil {
			t.Fatalf("输入 %q 应解析失败", raw)
		}
	}
}

// TestProbeUnavailable 验证二进制缺失时两个能力都返回 ErrUnavailable，供调用方降级。
func TestProbeUnavailable(t *testing.T) {
	prober := &Prober{lookPath: func(string) (string, error) {
		return "", errors.New("binary not found")
	}}
	ctx := context.Background()

	if _, err := prober.DurationMS(ctx, "clip.mp4"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("缺少 ffprobe 应返回 ErrUnavailable，实际 %v", err)
	}
	if err := prober.ExtractCover(ctx, "clip.mp4", "cover.jpg"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("缺少 ffmpeg 应返回 ErrUnavailable，实际 %v", err)
	}
}

// TestProbeEmptyPathDegrades 验证真实 PATH 为空（无 ffmpeg 环境）时同样降级且不 panic。
func TestProbeEmptyPathDegrades(t *testing.T) {
	t.Setenv("PATH", "")
	prober := NewProber()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := prober.DurationMS(ctx, "clip.mp4"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("空 PATH 下应返回 ErrUnavailable，实际 %v", err)
	}
	if err := prober.ExtractCover(ctx, "clip.mp4", "cover.jpg"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("空 PATH 下应返回 ErrUnavailable，实际 %v", err)
	}
}

// TestDurationBrokenBinary 验证二进制存在但执行失败时返回普通错误而非 ErrUnavailable，
// 便于调用方区分“环境缺失”与“文件本身异常”。
func TestDurationBrokenBinary(t *testing.T) {
	prober := &Prober{lookPath: func(string) (string, error) {
		return "definitely-missing-binary", nil
	}}
	_, err := prober.DurationMS(context.Background(), "clip.mp4")
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("期望普通执行错误，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "ffprobe") {
		t.Fatalf("错误信息应包含 ffprobe，实际 %v", err)
	}
}
