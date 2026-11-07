// probe.go 提供视频元信息探测：ffprobe 读取时长，ffmpeg 抽取首帧作为封面。
// 工具缺失时统一返回 ErrUnavailable，由调用方降级为“无时长、无封面”并继续主流程。
package media

import (
	"context"
	"encoding/json"
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

// VideoInfo 是一次 ffprobe 探测得到的关键视频元信息；时长优先取容器级 duration。
type VideoInfo struct {
	CodecName  string
	Width      int
	Height     int
	DurationMS int64
}

// probeStream 是 ffprobe 输出中的一路流信息。
type probeStream struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Duration  string `json:"duration"`
}

// probeOutput 是 ffprobe JSON 输出的最小映射；duration 以字符串返回。
type probeOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []probeStream `json:"streams"`
}

// ProbeVideo 用单次 ffprobe 调用读取首个视频流的编码、分辨率与时长，供上传硬校验使用。
// 二进制缺失或超时返回 ErrUnavailable，调用方据此降级；ffprobe 执行失败或输出不可解析返回普通错误。
func (p *Prober) ProbeVideo(ctx context.Context, path string) (*VideoInfo, error) {
	exe, err := p.lookPath("ffprobe")
	if err != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, exe,
		"-v", "error",
		"-show_entries", "format=duration:stream=codec_type,codec_name,width,height,duration",
		"-of", "json",
		path,
	).Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("ffprobe 探测视频失败: %w", err)
	}
	return ParseVideoInfo(out)
}

// ParseVideoInfo 解析 ffprobe 的 JSON 输出，返回首个视频流的编码、分辨率与时长。
// 没有视频流时返回错误；容器时长缺失时回退到视频流时长，两者都没有则时长记为 0。
func ParseVideoInfo(raw []byte) (*VideoInfo, error) {
	var parsed probeOutput
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析 ffprobe 输出失败: %w", err)
	}
	var video *probeStream
	for i := range parsed.Streams {
		if parsed.Streams[i].CodecType == "video" {
			video = &parsed.Streams[i]
			break
		}
	}
	if video == nil {
		return nil, fmt.Errorf("未检测到视频流")
	}

	info := &VideoInfo{
		CodecName: strings.ToLower(video.CodecName),
		Width:     video.Width,
		Height:    video.Height,
	}
	if ms, err := ParseDurationMS(parsed.Format.Duration); err == nil {
		info.DurationMS = ms
	} else if ms, err := ParseDurationMS(video.Duration); err == nil {
		info.DurationMS = ms
	}
	return info, nil
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
