// validation.go 收口两条上传通道共用的校验：kind/扩展名、大小上限、内容硬校验与哈希格式。
// 元信息校验在任何字节落盘之前完成；内容校验只在最终产物上跑一次，两条通道口径一致。
package httpupload

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"pulsefeed/internal/infra/media"
)

// resolveUploadKind 校验 kind 白名单与扩展名白名单，返回归一后的 kind 与小写扩展名。
// kind 为空时沿用单请求通道的缺省值 video。
func resolveUploadKind(kind, filename string) (string, string, error) {
	if kind == "" {
		kind = defaultKind
	}
	whitelist, exists := extWhitelist[kind]
	if !exists {
		return "", "", errors.New("非法的 kind")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" || !slices.Contains(whitelist, ext) {
		return "", "", errors.New("不支持的文件类型")
	}
	return kind, ext, nil
}

// sizeLimit 返回该类别的单文件大小上限与中文标签。
func sizeLimit(kind string, limits uploadLimits) (int64, string) {
	if kind == "video" {
		return limits.videoBytes, "视频"
	}
	return limits.imageBytes, "图片"
}

// validateUploadSize 校验声明大小是否超出类别上限，返回空串表示通过。
func validateUploadSize(kind string, size int64, limits uploadLimits) string {
	limit, label := sizeLimit(kind, limits)
	if size > limit {
		return fmt.Sprintf("%s文件超过大小上限（%dMB）", label, limit>>20)
	}
	return ""
}

// validateMediaContent 对最终产物做一次内容硬校验：视频走 ffprobe（时长/长边/编码），
// 图片走真实格式与可解码性。返回探测到的时长（非视频为 0）与可直接展示的错误原因。
// ffprobe 不可用时降级：记录 warning 并跳过视频硬校验，不阻断上传。
func validateMediaContent(ctx context.Context, prober videoProber, path, kind, ext string, limits uploadLimits) (int64, string) {
	if kind != "video" {
		if err := validateImage(path, ext); err != nil {
			return 0, err.Error()
		}
		return 0, ""
	}
	info, err := prober.ProbeVideo(ctx, path)
	switch {
	case err != nil && errors.Is(err, media.ErrUnavailable):
		log.Printf("warning: ffprobe 不可用，跳过视频时长/分辨率/编码白名单校验，仅保留扩展名校验: %s", filepath.Base(path))
		return 0, ""
	case err != nil:
		return 0, "视频文件内容非法，无法解析媒体信息"
	default:
		if message := validateVideoInfo(info, limits); message != "" {
			return 0, message
		}
		return info.DurationMS, ""
	}
}

// validSHA256 校验客户端期望哈希是否为 64 位小写十六进制。
func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
