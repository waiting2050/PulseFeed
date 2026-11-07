// handler.go 实现文件上传端点：按 kind 校验大小、扩展名与内容，并将文件落盘到 uploads/<kind>/。
// 视频经单次 ffprobe 探测校验时长、分辨率与编码白名单，并在工具可用时抽取首帧作为封面；
// ffprobe 不可用时保留原有扩展名降级路径，但日志写明跳过哪些校验。
package httpupload

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"pulsefeed/internal/infra/media"

	"github.com/gin-gonic/gin"
)

// defaultKind 是未提供 kind 表单字段时的默认类别。
const defaultKind = "video"

// 上传硬校验上限：视频单文件 512MB、图片 20MB、视频时长 10 分钟、视频长边 3840。
// 判定口径见各常量注释，测试可通过 Handler.limits 注入更小上限而不必构造大文件。
const (
	// MaxVideoBytes 是视频单文件大小上限（512MB）。
	MaxVideoBytes int64 = 512 << 20
	// MaxImageBytes 是封面/头像图片单文件大小上限（20MB）。
	MaxImageBytes int64 = 20 << 20
	// MaxVideoDurationMS 是视频时长上限（10 分钟）。
	MaxVideoDurationMS int64 = 10 * 60 * 1000
	// MaxVideoLongEdge 是视频分辨率上限，按长边判定：允许 2160×3840 竖屏 4K，
	// 拒绝长边超过 3840 的视频；短边口径会误伤竖屏 4K 内容。
	MaxVideoLongEdge = 3840
)

// allowedVideoCodecs 是 ffprobe codec_name 白名单；webm 容器常见 VP9 一并放行。
var allowedVideoCodecs = []string{"h264", "hevc", "vp9"}

// extWhitelist 是各类别允许的扩展名白名单，比较前统一转为小写。
var extWhitelist = map[string][]string{
	"video":  {".mp4", ".mov", ".webm"},
	"cover":  {".jpg", ".jpeg", ".png", ".webp"},
	"avatar": {".jpg", ".jpeg", ".png", ".webp"},
}

// videoProber 是视频探测能力端口；抽成接口便于测试注入假探测结果。
type videoProber interface {
	ProbeVideo(ctx context.Context, path string) (*media.VideoInfo, error)
	ExtractCover(ctx context.Context, videoPath, coverPath string) error
}

// uploadLimits 是上传硬校验的阈值集合，默认取上方的包级常量。
type uploadLimits struct {
	videoBytes      int64
	imageBytes      int64
	videoDurationMS int64
	videoLongEdge   int
}

// defaultUploadLimits 返回生产环境使用的默认上限。
func defaultUploadLimits() uploadLimits {
	return uploadLimits{
		videoBytes:      MaxVideoBytes,
		imageBytes:      MaxImageBytes,
		videoDurationMS: MaxVideoDurationMS,
		videoLongEdge:   MaxVideoLongEdge,
	}
}

// Handler 聚合上传相关端点。
type Handler struct {
	uploadDir string
	prober    videoProber
	limits    uploadLimits
}

// New 创建上传处理器；uploadDir 为落盘根目录，响应 URL 以 /uploads/<kind>/ 为前缀。
func New(uploadDir string) *Handler {
	return &Handler{uploadDir: uploadDir, prober: media.NewProber(), limits: defaultUploadLimits()}
}

// uploadResponse 是上传成功的响应数据；视频额外回传首帧封面与时长，工具缺失时两个字段省略。
type uploadResponse struct {
	URL        string `json:"url"`
	Kind       string `json:"kind"`
	CoverURL   string `json:"cover_url,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Upload 处理 POST /api/uploads：multipart 表单字段 file（文件）与 kind（video|cover|avatar，缺省 video）。
// 校验顺序为大小 → 扩展名 → ffprobe/图片解码，避免对大文件白跑探测；所有失败均返回 400 与可读原因。
func (h *Handler) Upload(c *gin.Context) {
	kind := c.PostForm("kind")
	if kind == "" {
		kind = defaultKind
	}
	whitelist, exists := extWhitelist[kind]
	if !exists {
		fail(c, http.StatusBadRequest, "非法的 kind")
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "缺少上传文件")
		return
	}

	// 第一步：大小上限。放在扩展名与 ffprobe 之前，超限文件不落盘、不探测。
	limit := h.limits.imageBytes
	label := "图片"
	if kind == "video" {
		limit = h.limits.videoBytes
		label = "视频"
	}
	if fileHeader.Size > limit {
		fail(c, http.StatusBadRequest, fmt.Sprintf("%s文件超过大小上限（%dMB）", label, limit>>20))
		return
	}

	// 第二步：扩展名白名单。
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if !slices.Contains(whitelist, ext) {
		fail(c, http.StatusBadRequest, "不支持的文件类型")
		return
	}

	filename := fmt.Sprintf("%s-%s%s", time.Now().Format("20060102150405"), randomSuffix(), ext)
	dst := filepath.Join(h.uploadDir, kind, filename)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fail(c, http.StatusInternalServerError, "创建上传目录失败")
		return
	}
	if err := c.SaveUploadedFile(fileHeader, dst); err != nil {
		fail(c, http.StatusInternalServerError, "保存上传文件失败")
		return
	}

	if kind == "video" {
		// 第三步：ffprobe 探测时长、分辨率与编码；环境缺失时降级并写明跳过项。
		var durationMS int64
		info, err := h.prober.ProbeVideo(c.Request.Context(), dst)
		switch {
		case err != nil && errors.Is(err, media.ErrUnavailable):
			log.Printf("warning: ffprobe 不可用，跳过视频时长/分辨率/编码白名单校验，仅保留扩展名校验: %s", fileHeader.Filename)
		case err != nil:
			_ = os.Remove(dst)
			fail(c, http.StatusBadRequest, "视频文件内容非法，无法解析媒体信息")
			return
		default:
			if message := validateVideoInfo(info, h.limits); message != "" {
				_ = os.Remove(dst)
				fail(c, http.StatusBadRequest, message)
				return
			}
			durationMS = info.DurationMS
		}
		// 封面抽取同样允许降级：ffmpeg 缺失或抽帧失败只记录 warning，上传照常成功。
		coverURL, enrichedDuration := h.enrichVideo(c.Request.Context(), dst, strings.TrimSuffix(filename, ext)+".jpg", durationMS)
		ok(c, http.StatusCreated, uploadResponse{
			URL:        "/uploads/video/" + filename,
			Kind:       kind,
			CoverURL:   coverURL,
			DurationMS: enrichedDuration,
		})
		return
	}

	// 图片：第三步校验真实格式与可解码性，防止改扩展名绕过。
	if err := validateImage(dst, ext); err != nil {
		_ = os.Remove(dst)
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	ok(c, http.StatusCreated, uploadResponse{
		URL:  "/uploads/" + kind + "/" + filename,
		Kind: kind,
	})
}

// validateVideoInfo 按上限校验探测结果，返回空串表示通过；否则返回可直接展示的中文原因。
func validateVideoInfo(info *media.VideoInfo, limits uploadLimits) string {
	if info == nil {
		return "视频文件内容非法，无法解析媒体信息"
	}
	if info.DurationMS <= 0 {
		return "视频时长无法识别，文件可能已损坏"
	}
	if info.DurationMS > limits.videoDurationMS {
		return fmt.Sprintf("视频时长超过上限（%d 分钟）", limits.videoDurationMS/60000)
	}
	if info.Width <= 0 || info.Height <= 0 {
		return "未检测到视频画面，文件可能已损坏"
	}
	longEdge := info.Width
	if info.Height > longEdge {
		longEdge = info.Height
	}
	if longEdge > limits.videoLongEdge {
		return fmt.Sprintf("视频分辨率超过上限（长边 %d）", limits.videoLongEdge)
	}
	if !slices.Contains(allowedVideoCodecs, info.CodecName) {
		return fmt.Sprintf("不支持的视频编码 %s（仅支持 H.264/HEVC/VP9）", info.CodecName)
	}
	return ""
}

// enrichVideo 为视频抽取首帧，返回封面 URL 与毫秒时长；时长来自前一步探测结果。
// 无 ffmpeg 时返回空封面并写明日志原因，不阻断上传；封面写入失败同样只降级。
func (h *Handler) enrichVideo(ctx context.Context, videoPath, coverName string, durationMS int64) (string, int64) {
	coverPath := filepath.Join(h.uploadDir, "cover", coverName)
	if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
		log.Printf("warning: 创建封面目录失败，跳过首帧封面: %v", err)
		return "", durationMS
	}

	if err := h.prober.ExtractCover(ctx, videoPath, coverPath); err != nil {
		if errors.Is(err, media.ErrUnavailable) {
			log.Printf("warning: ffmpeg 不可用，跳过首帧封面: %s", filepath.Base(videoPath))
		} else {
			log.Printf("warning: 抽取首帧失败，跳过封面: %v", err)
		}
		return "", durationMS
	}
	return "/uploads/cover/" + coverName, durationMS
}

// validateImage 校验图片真实格式与可解码性：jpg/png 用标准库解码头部，webp 校验 RIFF 容器与 VP8 块。
func validateImage(path, ext string) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("图片文件读取失败")
	}
	defer file.Close()

	switch ext {
	case ".jpg", ".jpeg":
		_, format, err := image.DecodeConfig(file)
		if err != nil || format != "jpeg" {
			return errors.New("图片文件无法解码或格式与扩展名不符")
		}
	case ".png":
		_, format, err := image.DecodeConfig(file)
		if err != nil || format != "png" {
			return errors.New("图片文件无法解码或格式与扩展名不符")
		}
	case ".webp":
		data, err := os.ReadFile(path)
		if err != nil || !isValidWebP(data) {
			return errors.New("图片文件无法解码或格式与扩展名不符")
		}
	}
	return nil
}

// isValidWebP 校验 WebP 容器：RIFF 声明长度不得超出实际内容，且至少包含一个 VP8/VP8L/VP8X 块。
// 标准库不内置 WebP 解码器，此处做容器级可解码性检查。
func isValidWebP(data []byte) bool {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	declared := int(binary.LittleEndian.Uint32(data[4:8]))
	if declared > len(data)-8 {
		return false
	}
	for offset := 12; offset+8 <= len(data); {
		chunk := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if size < 0 || offset+8+size > len(data) {
			return false
		}
		if chunk == "VP8 " || chunk == "VP8L" || chunk == "VP8X" {
			return true
		}
		offset += 8 + size + (size & 1)
	}
	return false
}

// randomSuffix 生成 8 位十六进制随机串，与同秒时间戳拼接避免文件名冲突。
func randomSuffix() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 不可用时退化为纳秒时间戳，冲突概率仍可忽略。
		return fmt.Sprintf("%08x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// ok 以统一信封返回成功响应。
func ok(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"code": 0, "message": "ok", "data": data})
}

// fail 以统一信封返回错误响应，code 与 HTTP 状态码保持一致。
func fail(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"code": status, "message": message})
}
