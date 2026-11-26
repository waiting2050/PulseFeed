// upload_test.go 覆盖上传端点按 kind（video|cover|avatar）落盘与 URL 回显、
// 非法扩展名/非法 kind/缺少文件返回 400，以及 ffprobe 不可用时的扩展名降级路径。
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

// uploadTPost 构造 multipart 表单并向 POST /api/uploads 发起请求；filename 为空时不写 file 字段。
func uploadTPost(t *testing.T, dir, kind, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/uploads", New(dir).Upload)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("kind", kind); err != nil {
		t.Fatalf("写入 kind 字段失败: %v", err)
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("创建文件字段失败: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("写入文件内容失败: %v", err)
		}
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

// uploadTDecode 解析统一响应信封，返回业务码与 data 字典。
func uploadTDecode(t *testing.T, recorder *httptest.ResponseRecorder) (int, map[string]string) {
	t.Helper()
	var resp struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非合法 JSON: %v, body=%s", err, recorder.Body.String())
	}
	if resp.Data == nil {
		resp.Data = map[string]string{}
	}
	return resp.Code, resp.Data
}

// TestUploadAllKindsPersist 验证三类 kind 的合法扩展名均能落盘并返回 /uploads/<kind>/... URL。
func TestUploadAllKindsPersist(t *testing.T) {
	// 置空 PATH 让视频探测走 C7 降级路径，避免依赖本机是否安装 ffprobe。
	t.Setenv("PATH", "")
	dir := t.TempDir()

	cases := []struct {
		kind     string
		filename string
		content  []byte
	}{
		{kind: "video", filename: "clip.MP4", content: []byte("fake-mp4-payload")},
		{kind: "cover", filename: "cover.jpeg", content: testJPEG(t)},
		{kind: "avatar", filename: "me.webp", content: testWEBP()},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			recorder := uploadTPost(t, dir, tc.kind, tc.filename, tc.content)
			if recorder.Code != http.StatusCreated {
				t.Fatalf("期望 201，实际 %d，body=%s", recorder.Code, recorder.Body.String())
			}
			code, data := uploadTDecode(t, recorder)
			if code != 0 {
				t.Fatalf("期望业务码 0，实际 %d", code)
			}
			wantPrefix := "/uploads/" + tc.kind + "/"
			if !strings.HasPrefix(data["url"], wantPrefix) {
				t.Fatalf("返回 URL 应符合 %s*，实际 %s", wantPrefix, data["url"])
			}
			if data["kind"] != tc.kind {
				t.Fatalf("期望 kind=%s，实际 %s", tc.kind, data["kind"])
			}

			stored, err := os.ReadFile(filepath.Join(dir, tc.kind, strings.TrimPrefix(data["url"], wantPrefix)))
			if err != nil {
				t.Fatalf("读取落盘文件失败: %v", err)
			}
			if !bytes.Equal(stored, tc.content) {
				t.Fatalf("落盘内容与上传内容不一致")
			}
		})
	}
}

// TestUploadRejectsBadExtension 验证白名单外扩展名返回 400 且不产生文件。
func TestUploadRejectsBadExtension(t *testing.T) {
	dir := t.TempDir()
	recorder := uploadTPost(t, dir, "cover", "notes.txt", []byte("text"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	code, data := uploadTDecode(t, recorder)
	if code != http.StatusBadRequest || data["url"] != "" {
		t.Fatalf("错误响应不应携带 URL，code=%d data=%v", code, data)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "cover")); err == nil && len(entries) > 0 {
		t.Fatalf("非法扩展名不应落盘，实际存在 %d 个文件", len(entries))
	}
}

// TestUploadRejectsBadKind 验证 kind 不在 video|cover|avatar 内时返回 400。
func TestUploadRejectsBadKind(t *testing.T) {
	recorder := uploadTPost(t, t.TempDir(), "audio", "sound.mp3", []byte("mp3"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestUploadRejectsMissingFile 验证 kind 合法但缺少 file 字段时返回 400。
func TestUploadRejectsMissingFile(t *testing.T) {
	recorder := uploadTPost(t, t.TempDir(), "video", "", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少文件期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestUploadVideoDegradesWithoutFFProbe 验证 ffprobe 不可用时视频上传仍按扩展名成功落盘。
func TestUploadVideoDegradesWithoutFFProbe(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	content := []byte("fake-mp4-payload")
	recorder := uploadTPost(t, dir, "video", "clip.mp4", content)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("ffprobe 不可用时应降级放行，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	_, data := uploadTDecode(t, recorder)
	url := data["url"]
	if !strings.HasPrefix(url, "/uploads/video/") || !strings.HasSuffix(url, ".mp4") {
		t.Fatalf("返回 URL 不符合 /uploads/video/*.mp4 形态: %s", url)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "video", strings.TrimPrefix(url, "/uploads/video/")))
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("落盘内容与上传内容不一致")
	}
}
