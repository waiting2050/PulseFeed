// handler.go 实现文件上传端点：按 kind 校验扩展名白名单并将文件落盘到 uploads/<kind>/；
// 视频文件额外经 ffprobe 探测内容，并在工具可用时读取时长、抽取首帧作为封面（C7）。
package httpupload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"pulsefeed/internal/infra/media"

	"github.com/gin-gonic/gin"
)

// defaultKind 是未提供 kind 表单字段时的默认类别。
const defaultKind = "video"

// extWhitelist 是各类别允许的扩展名白名单，比较前统一转为小写。
var extWhitelist = map[string][]string{
	"video":  {".mp4", ".mov", ".webm"},
	"cover":  {".jpg", ".jpeg", ".png", ".webp"},
	"avatar": {".jpg", ".jpeg", ".png", ".webp"},
}

// errProbeUnavailable 表示 ffprobe 缺失、无法执行或探测超时，调用方据此走降级路径。
var errProbeUnavailable = errors.New("ffprobe 不可用")

// Handler 聚合上传相关端点。
type Handler struct {
	uploadDir string
	prober    *media.Prober
}

// New 创建上传处理器；uploadDir 为落盘根目录，响应 URL 以 /uploads/<kind>/ 为前缀。
func New(uploadDir string) *Handler {
	return &Handler{uploadDir: uploadDir, prober: media.NewProber()}
}

// uploadResponse 是上传成功的响应数据；视频额外回传首帧封面与时长，工具缺失时两个字段省略。
type uploadResponse struct {
	URL        string `json:"url"`
	Kind       string `json:"kind"`
	CoverURL   string `json:"cover_url,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Upload 处理 POST /api/uploads：multipart 表单字段 file（文件）与 kind（video|cover|avatar，缺省 video）。
// 落盘文件名形如 20060102150405-1a2b3c4d.mp4；成功返回 201 与可经静态路由回读的 URL。
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

	// 视频内容校验（C7）：ffprobe 缺失或超时降级为扩展名校验；探测明确拒绝则删除文件并返回 400。
	var coverURL string
	var durationMS int64
	if kind == "video" {
		if err := probeVideo(c.Request.Context(), dst); err != nil {
			if errors.Is(err, errProbeUnavailable) {
				log.Printf("warning: %v，%s 降级为扩展名校验", err, fileHeader.Filename)
			} else {
				_ = os.Remove(dst)
				fail(c, http.StatusBadRequest, "视频文件内容非法")
				return
			}
		}
		// 首帧与时长探测同样允许降级：任一工具缺失只记录 warning，上传照常成功。
		coverURL, durationMS = h.enrichVideo(c.Request.Context(), dst, strings.TrimSuffix(filename, ext)+".jpg")
	}

	ok(c, http.StatusCreated, uploadResponse{
		URL:        "/uploads/" + kind + "/" + filename,
		Kind:       kind,
		CoverURL:   coverURL,
		DurationMS: durationMS,
	})
}

// enrichVideo 为视频抽取首帧并读取时长，返回封面 URL 与毫秒时长。
// 无 ffmpeg/ffprobe 时返回零值并写明日志原因，不阻断上传；封面写入失败同样只降级。
func (h *Handler) enrichVideo(ctx context.Context, videoPath, coverName string) (string, int64) {
	coverPath := filepath.Join(h.uploadDir, "cover", coverName)
	if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
		log.Printf("warning: 创建封面目录失败，跳过首帧与时长: %v", err)
		return "", 0
	}

	var durationMS int64
	if ms, err := h.prober.DurationMS(ctx, videoPath); err != nil {
		if errors.Is(err, media.ErrUnavailable) {
			log.Printf("warning: ffprobe 不可用，跳过时长解析: %s", filepath.Base(videoPath))
		} else {
			log.Printf("warning: 读取视频时长失败，跳过时长: %v", err)
		}
	} else {
		durationMS = ms
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

// randomSuffix 生成 8 位十六进制随机串，与同秒时间戳拼接避免文件名冲突。
func randomSuffix() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 不可用时退化为纳秒时间戳，冲突概率仍可忽略。
		return fmt.Sprintf("%08x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// probeVideo 调用 ffprobe 校验视频文件可读性，5 秒超时；二进制缺失或超时返回 errProbeUnavailable。
func probeVideo(ctx context.Context, path string) error {
	exe, err := exec.LookPath("ffprobe")
	if err != nil {
		return errProbeUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-v", "error", "-show_entries", "format=format_name", "-of", "default=noprint_wrappers=1:nokey=1", path)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return errProbeUnavailable
		}
		return fmt.Errorf("ffprobe 校验未通过: %w", err)
	}
	return nil
}

// ok 以统一信封返回成功响应。
func ok(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"code": 0, "message": "ok", "data": data})
}

// fail 以统一信封返回错误响应，code 与 HTTP 状态码保持一致。
func fail(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"code": status, "message": message})
}
