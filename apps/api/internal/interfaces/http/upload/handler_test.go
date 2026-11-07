// handler_test.go 覆盖上传端点的硬校验：大小、扩展名、图片解码、视频探测与 ffprobe 降级路径。
package httpupload

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pulsefeed/internal/infra/media"

	"github.com/gin-gonic/gin"
)

// testPNG 生成一张 2×2 的真实 PNG，供图片解码校验用例使用。
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

// testJPEG 生成一张 2×2 的真实 JPEG，供格式匹配用例使用。
func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("生成 JPEG 失败: %v", err)
	}
	return buf.Bytes()
}

// testWEBP 构造带 VP8L 块的 RIFF/WEBP 容器，供容器级校验用例使用。
func testWEBP() []byte {
	chunk := []byte{0x2f, 0x00, 0x00, 0x00, 0x00}
	out := make([]byte, 0, 12+8+len(chunk))
	out = append(out, 'R', 'I', 'F', 'F')
	size := uint32(4 + 8 + len(chunk))
	out = append(out, byte(size), byte(size>>8), byte(size>>16), byte(size>>24))
	out = append(out, 'W', 'E', 'B', 'P', 'V', 'P', '8', 'L')
	out = append(out, byte(len(chunk)), 0, 0, 0)
	out = append(out, chunk...)
	return out
}

// fakeProber 注入视频探测结果，避免测试依赖本机 ffprobe/ffmpeg。
type fakeProber struct {
	info     *media.VideoInfo
	probeErr error
	coverErr error
}

// ProbeVideo 返回预设的探测结果。
func (f *fakeProber) ProbeVideo(_ context.Context, _ string) (*media.VideoInfo, error) {
	return f.info, f.probeErr
}

// ExtractCover 返回预设的抽帧结果。
func (f *fakeProber) ExtractCover(_ context.Context, _, _ string) error {
	return f.coverErr
}

// doUploadWith 使用指定 Handler 构造 multipart 表单并请求上传端点；kind 为空串时不写 kind 字段。
func doUploadWith(t *testing.T, handler *Handler, kind, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/uploads", handler.Upload)

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

// doUpload 使用默认上限的 Handler 发起上传。
func doUpload(t *testing.T, dir, kind, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	return doUploadWith(t, New(dir), kind, filename, content)
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

// decodeEnvelopeData 解析统一响应信封，保留 data 的原始 JSON 类型（时长是数字）。
func decodeEnvelopeData(t *testing.T, recorder *httptest.ResponseRecorder) (int, map[string]any) {
	t.Helper()
	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
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

// TestUploadRejectsOversizeVideo 用缩小后的上限验证超大小视频被 400 拒绝且不落盘。
func TestUploadRejectsOversizeVideo(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	handler := New(dir)
	handler.limits.videoBytes = 8
	recorder := doUploadWith(t, handler, "video", "clip.mp4", []byte("123456789"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "大小上限") {
		t.Fatalf("错误信息应说明大小上限，body=%s", recorder.Body.String())
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "video")); err == nil && len(entries) > 0 {
		t.Fatalf("超限文件不应落盘，实际存在 %d 个文件", len(entries))
	}
}

// TestUploadRejectsOversizeImage 用缩小后的上限验证超大小图片被 400 拒绝。
func TestUploadRejectsOversizeImage(t *testing.T) {
	dir := t.TempDir()
	handler := New(dir)
	handler.limits.imageBytes = 8
	recorder := doUploadWith(t, handler, "cover", "cover.png", []byte("123456789"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "大小上限") {
		t.Fatalf("错误信息应说明大小上限，body=%s", recorder.Body.String())
	}
}

// TestUploadRejectsLongVideo 验证探测时长超过 10 分钟返回 400 并删除已落盘文件。
func TestUploadRejectsLongVideo(t *testing.T) {
	dir := t.TempDir()
	handler := New(dir)
	handler.prober = &fakeProber{info: &media.VideoInfo{
		CodecName: "h264", Width: 1920, Height: 1080, DurationMS: MaxVideoDurationMS + 1000,
	}}
	recorder := doUploadWith(t, handler, "video", "clip.mp4", []byte("fake-mp4"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "时长超过上限") {
		t.Fatalf("错误信息应说明时长上限，body=%s", recorder.Body.String())
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "video")); err == nil && len(entries) > 0 {
		t.Fatalf("校验失败应删除落盘文件，实际存在 %d 个", len(entries))
	}
}

// TestUploadRejectsHighResolution 验证长边超过 3840 的视频被拒绝。
func TestUploadRejectsHighResolution(t *testing.T) {
	handler := New(t.TempDir())
	handler.prober = &fakeProber{info: &media.VideoInfo{
		CodecName: "h264", Width: 4096, Height: 2160, DurationMS: 90_000,
	}}
	recorder := doUploadWith(t, handler, "video", "clip.mp4", []byte("fake-mp4"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "分辨率超过上限") {
		t.Fatalf("错误信息应说明分辨率上限，body=%s", recorder.Body.String())
	}
}

// TestUploadRejectsUnsupportedCodec 验证白名单外编码返回 400。
func TestUploadRejectsUnsupportedCodec(t *testing.T) {
	handler := New(t.TempDir())
	handler.prober = &fakeProber{info: &media.VideoInfo{
		CodecName: "mpeg4", Width: 1280, Height: 720, DurationMS: 30_000,
	}}
	recorder := doUploadWith(t, handler, "video", "clip.mp4", []byte("fake-mp4"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "不支持的视频编码") {
		t.Fatalf("错误信息应说明编码不支持，body=%s", recorder.Body.String())
	}
}

// TestUploadAcceptsValidVideo 验证白名单编码、时长与分辨率合规的视频正常通过并回传时长/封面。
func TestUploadAcceptsValidVideo(t *testing.T) {
	dir := t.TempDir()
	handler := New(dir)
	handler.prober = &fakeProber{info: &media.VideoInfo{
		CodecName: "h264", Width: 1920, Height: 1080, DurationMS: 90_000,
	}}
	recorder := doUploadWith(t, handler, "video", "clip.mp4", []byte("fake-mp4"))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	code, data := decodeEnvelopeData(t, recorder)
	if code != 0 {
		t.Fatalf("期望业务码 0，实际 %d", code)
	}
	if data["duration_ms"] != float64(90_000) {
		t.Fatalf("期望回传 duration_ms=90000，实际 %v", data["duration_ms"])
	}
	if !strings.HasPrefix(data["cover_url"].(string), "/uploads/cover/") {
		t.Fatalf("期望回传封面 URL，实际 %v", data["cover_url"])
	}
}

// TestUploadVideoDegradesWhenProbeUnavailable 验证 ffprobe 不可用时跳过硬校验并保留扩展名降级。
func TestUploadVideoDegradesWhenProbeUnavailable(t *testing.T) {
	dir := t.TempDir()
	handler := New(dir)
	handler.prober = &fakeProber{probeErr: media.ErrUnavailable, coverErr: media.ErrUnavailable}
	recorder := doUploadWith(t, handler, "video", "clip.mp4", []byte("fake-mp4"))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("ffprobe 不可用时应降级放行，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "video")); err != nil || len(entries) != 1 {
		t.Fatalf("降级放行后应落盘 1 个文件，实际 err=%v entries=%d", err, len(entries))
	}
}

// TestUploadCoverCreated 验证正常封面上传返回 201、URL 形态正确且内容落盘一致。
func TestUploadCoverCreated(t *testing.T) {
	dir := t.TempDir()
	content := testPNG(t)
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

// TestUploadRejectsUndecodableImage 验证内容不是有效 PNG 时返回 400 并删除落盘文件。
func TestUploadRejectsUndecodableImage(t *testing.T) {
	dir := t.TempDir()
	recorder := doUpload(t, dir, "cover", "broken.png", []byte("not-a-png"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "无法解码") {
		t.Fatalf("错误信息应说明解码失败，body=%s", recorder.Body.String())
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "cover")); err == nil && len(entries) > 0 {
		t.Fatalf("解码失败应删除落盘文件，实际存在 %d 个", len(entries))
	}
}

// TestUploadRejectsMismatchedImage 验证 JPEG 内容改名为 .png 时按真实格式拒绝。
func TestUploadRejectsMismatchedImage(t *testing.T) {
	recorder := doUpload(t, t.TempDir(), "avatar", "me.png", testJPEG(t))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "格式与扩展名不符") {
		t.Fatalf("错误信息应说明格式不符，body=%s", recorder.Body.String())
	}
}

// TestUploadDefaultsToVideoKind 验证缺省 kind 归入 video 目录；置空 PATH 强制走降级路径，
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
