package upload

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/shared/httpx"
)

const maxUploadBytes = 200 << 20

var allowedExtensions = map[string]map[string]bool{
	"video":  {".mp4": true, ".mov": true, ".webm": true},
	"cover":  {".jpg": true, ".jpeg": true, ".png": true, ".webp": true},
	"avatar": {".jpg": true, ".jpeg": true, ".png": true, ".webp": true},
}

var maxSizes = map[string]int64{
	"video":  200 << 20,
	"cover":  5 << 20,
	"avatar": 2 << 20,
}

type Handler struct {
	dir    string
	logger *slog.Logger
}

type uploadResponse struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
}

func NewHandler(dir string, logger *slog.Logger) *Handler {
	return &Handler{dir: dir, logger: logger}
}

func (h *Handler) RegisterRoutes(api gin.IRouter, auth gin.HandlerFunc) {
	protected := api.Group("")
	protected.Use(auth)
	protected.POST("/uploads", h.Create)
}

func (h *Handler) Create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes+(1<<20))

	kind := strings.TrimSpace(c.PostForm("kind"))
	extensions, ok := allowedExtensions[kind]
	if !ok {
		httpx.BadRequest(c, "上传类型仅支持 video、cover、avatar")
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		httpx.BadRequest(c, "缺少上传文件或文件超过大小限制")
		return
	}
	if file.Size <= 0 || file.Size > maxSizes[kind] {
		httpx.BadRequest(c, fmt.Sprintf("文件大小需在 1 字节到 %d MB 之间", maxSizes[kind]>>20))
		return
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	if !extensions[ext] {
		httpx.BadRequest(c, "文件扩展名不被允许")
		return
	}

	dir := filepath.Join(h.dir, kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.logger.Error("create upload directory failed", "error", err)
		httpx.Internal(c, "上传目录创建失败")
		return
	}

	filename, err := randomFilename(ext)
	if err != nil {
		h.logger.Error("generate upload filename failed", "error", err)
		httpx.Internal(c, "上传文件命名失败")
		return
	}

	if err := c.SaveUploadedFile(file, filepath.Join(dir, filename)); err != nil {
		h.logger.Error("save uploaded file failed", "error", err)
		httpx.Internal(c, "文件保存失败")
		return
	}

	httpx.Created(c, uploadResponse{
		URL:      path.Join("/uploads", kind, filename),
		Filename: filename,
		Size:     file.Size,
		Kind:     kind,
	})
}

func randomFilename(ext string) (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s%s", time.Now().UnixNano(), hex.EncodeToString(buffer), ext), nil
}
