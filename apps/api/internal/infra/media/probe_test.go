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

// TestParseVideoInfo 验证从 ffprobe JSON 取首个视频流的编码、分辨率与容器时长。
func TestParseVideoInfo(t *testing.T) {
	raw := []byte(`{
		"streams": [
			{"codec_type": "audio", "codec_name": "aac"},
			{"codec_type": "video", "codec_name": "H264", "width": 1080, "height": 1920}
		],
		"format": {"duration": "125.5"}
	}`)
	info, err := ParseVideoInfo(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.CodecName != "h264" || info.Width != 1080 || info.Height != 1920 || info.DurationMS != 125500 {
		t.Fatalf("解析结果不符: %+v", info)
	}
}

// TestParseVideoInfoFallsBackToStreamDuration 验证容器时长缺失时回退到视频流时长。
func TestParseVideoInfoFallsBackToStreamDuration(t *testing.T) {
	raw := []byte(`{
		"streams": [{"codec_type": "video", "codec_name": "hevc", "width": 2160, "height": 3840, "duration": "42.0"}],
		"format": {}
	}`)
	info, err := ParseVideoInfo(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.DurationMS != 42000 || info.CodecName != "hevc" {
		t.Fatalf("回退时长结果不符: %+v", info)
	}
}

// TestParseVideoInfoRejectsNoVideoStream 验证没有视频流或输出非法时返回错误。
func TestParseVideoInfoRejectsNoVideoStream(t *testing.T) {
	for _, raw := range []string{`{"streams":[{"codec_type":"audio"}],"format":{"duration":"3"}}`, `not-json`} {
		if _, err := ParseVideoInfo([]byte(raw)); err == nil {
			t.Fatalf("输入 %q 应解析失败", raw)
		}
	}
}

// TestProbeVideoUnavailable 验证 ffprobe 缺失时 ProbeVideo 返回 ErrUnavailable 供调用方降级。
func TestProbeVideoUnavailable(t *testing.T) {
	prober := &Prober{lookPath: func(string) (string, error) {
		return "", errors.New("binary not found")
	}}
	if _, err := prober.ProbeVideo(context.Background(), "clip.mp4"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("缺少 ffprobe 应返回 ErrUnavailable，实际 %v", err)
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
