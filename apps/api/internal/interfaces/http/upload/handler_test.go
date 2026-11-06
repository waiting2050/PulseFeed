// handler_test.go 覆盖上传端点的扩展名白名单、非法 kind 拦截与正常落盘回读路径。
package httpupload

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// doUpload 构造 multipart 表单并向 POST /api/uploads 发起请求；kind 为空串时不写 kind 字段。
func doUpload(t *testing.T, dir, kind, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/uploads", New(dir).Upload)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if kind != "" {
		if err := writer.WriteField("kind", kind); err != nil {
			t.Fatalf("写入 kind 字段失败: %v", err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建文件字段失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写入文件内容失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/uploads", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

// decodeEnvelope 解析统一响应信封，返回业务码与 data 字典。
func decodeEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) (int, map[string]string) {
	t.Helper()
	var resp struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Data    map[string]string `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非合法 JSON: %v, body=%s", err, recorder.Body.String())
	}
	return resp.Code, resp.Data
}

// TestUploadRejectsIllegalExtension 验证白名单外扩展名返回 400 且不落盘。
func TestUploadRejectsIllegalExtension(t *testing.T) {
	dir := t.TempDir()
	recorder := doUpload(t, dir, "video", "evil.exe", []byte("MZ"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "video")); err == nil && len(entries) > 0 {
		t.Fatalf("非法扩展名不应落盘，实际存在 %d 个文件", len(entries))
	}
}

// TestUploadRejectsUnknownKind 验证白名单外 kind 返回 400。
func TestUploadRejectsUnknownKind(t *testing.T) {
	recorder := doUpload(t, t.TempDir(), "bogus", "a.png", []byte("png"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestUploadCoverCreated 验证正常封面上传返回 201、URL 形态正确且内容落盘一致。
func TestUploadCoverCreated(t *testing.T) {
	dir := t.TempDir()
	content := []byte("\x89PNG\r\n\x1a\nfake-png-payload")
	recorder := doUpload(t, dir, "cover", "Cover.PNG", content)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	code, data := decodeEnvelope(t, recorder)
	if code != 0 {
		t.Fatalf("期望业务码 0，实际 %d", code)
	}
	url := data["url"]
	if !strings.HasPrefix(url, "/uploads/cover/") || !strings.HasSuffix(url, ".png") {
		t.Fatalf("返回 URL 不符合 /uploads/cover/*.png 形态: %s", url)
	}
	if data["kind"] != "cover" {
		t.Fatalf("期望 kind=cover，实际 %s", data["kind"])
	}
	stored, err := os.ReadFile(filepath.Join(dir, "cover", strings.TrimPrefix(url, "/uploads/cover/")))
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("落盘内容与上传内容不一致")
	}
}

// TestUploadDefaultsToVideoKind 验证缺省 kind 归入 video 目录；置空 PATH 强制走 C7 降级路径，
// 同时验证无 ffmpeg/ffprobe 时响应不含 cover_url 与 duration_ms，上传仍然成功。
func TestUploadDefaultsToVideoKind(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	recorder := doUpload(t, dir, "", "clip.mp4", []byte("fake-mp4-payload"))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	_, data := decodeEnvelope(t, recorder)
	if !strings.HasPrefix(data["url"], "/uploads/video/") {
		t.Fatalf("缺省 kind 应归入 video 目录: %s", data["url"])
	}
	if data["cover_url"] != "" || data["duration_ms"] != "" {
		t.Fatalf("无 ffmpeg 环境应省略封面与时长，实际 cover=%q duration=%q", data["cover_url"], data["duration_ms"])
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "cover")); err == nil && len(entries) > 0 {
		t.Fatalf("无 ffmpeg 环境不应生成封面文件，实际 %d 个", len(entries))
	}
}
