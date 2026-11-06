// probe.go 提供视频元信息探测：ffprobe 读取时长，ffmpeg 抽取首帧作为封面。
// 工具缺失时统一返回 ErrUnavailable，由调用方降级为“无时长、无封面”并继续主流程。
package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable 表示本机缺少 ffprobe/ffmpeg，调用方应跳过探测而不阻断上传与发布。
var ErrUnavailable = errors.New("ffmpeg/ffprobe 不可用")

const (
	// probeTimeout 限制单次时长探测耗时，避免异常文件拖住上传请求。
	probeTimeout = 5 * time.Second
	// coverTimeout 限制单次抽帧耗时。
	coverTimeout = 10 * time.Second
)

// Prober 封装外部命令调用；lookPath 可注入，便于测试无 ffmpeg 环境下的降级路径。
type Prober struct {
	lookPath func(string) (string, error)
}

// NewProber 创建使用系统 PATH 查找 ffmpeg/ffprobe 的探测器。
func NewProber() *Prober {
	return &Prober{lookPath: exec.LookPath}
}

// DurationMS 用 ffprobe 读取媒体时长并换算为毫秒。
// 二进制缺失返回 ErrUnavailable；超时同样按不可用处理，避免把超时误判为内容非法。
func (p *Prober) DurationMS(ctx context.Context, path string) (int64, error) {
	exe, err := p.lookPath("ffprobe")
	if err != nil {
		return 0, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, exe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, ErrUnavailable
		}
		return 0, fmt.Errorf("ffprobe 读取时长失败: %w", err)
	}
	return ParseDurationMS(string(out))
}

// ParseDurationMS 解析 ffprobe 输出的秒数（可含空白与换行）并四舍五入为毫秒。
// 空值、N/A、负数与非数字均视为无法解析。
func ParseDurationMS(raw string) (int64, error) {
	text := strings.TrimSpace(raw)
	if text == "" || text == "N/A" {
		return 0, fmt.Errorf("无法解析时长: %q", raw)
	}
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("无法解析时长: %q", raw)
	}
	return int64(math.Round(seconds * 1000)), nil
}

// ExtractCover 用 ffmpeg 抽取首帧写为 JPEG；二进制缺失返回 ErrUnavailable。
// 调用方负责提前创建输出目录；抽帧失败不删除视频文件。
func (p *Prober) ExtractCover(ctx context.Context, videoPath, coverPath string) error {
	exe, err := p.lookPath("ffmpeg")
	if err != nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, coverTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe,
		"-y", "-v", "error",
		"-i", videoPath,
		"-frames:v", "1",
		"-q:v", "3",
		coverPath,
	)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		return fmt.Errorf("抽取首帧失败: %w", err)
	}
	return nil
}
