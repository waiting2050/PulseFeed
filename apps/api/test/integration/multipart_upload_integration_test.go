//go:build integration

// multipart_upload_integration_test.go 用独立 schema 跑分片上传全链路（init → 分片 → complete），
// 校验 pf_upload_sessions / pf_upload_parts 的行与状态流转，并验证重复 complete 幂等。
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	infraupload "pulsefeed/internal/infra/persistence/upload"
	"pulsefeed/internal/interfaces/http/middleware"
	httpupload "pulsefeed/internal/interfaces/http/upload"

	"github.com/gin-gonic/gin"
)

// integrationPNG 生成一张可解码的 2×2 PNG，供图片内容校验通过。
func integrationPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

// integrationMultipartEngine 注册分片端点并注入固定登录用户。
func integrationMultipartEngine(t *testing.T, handler *httpupload.Handler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserIDKey, int64(1))
		c.Next()
	})
	engine.POST("/api/uploads/multipart", handler.InitMultipart)
	engine.PUT("/api/uploads/multipart/:uploadId/parts/:partNumber", handler.UploadPart)
	engine.GET("/api/uploads/multipart/:uploadId", handler.GetMultipart)
	engine.POST("/api/uploads/multipart/:uploadId/complete", handler.CompleteMultipart)
	engine.DELETE("/api/uploads/multipart/:uploadId", handler.CancelMultipart)
	return engine
}

// integrationJSON 发起 JSON 请求并返回响应。
func integrationJSON(t *testing.T, engine *gin.Engine, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		payload = encoded
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

// integrationEnvelope 解析统一信封。
func integrationEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) (int, string, map[string]any) {
	t.Helper()
	var resp struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非合法 JSON: %v, body=%s", err, recorder.Body.String())
	}
	return recorder.Code, resp.Message, resp.Data
}

// TestMultipartUploadEndToEnd 跑通独立 schema 下的完整分片链路并校验落库结果。
func TestMultipartUploadEndToEnd(t *testing.T) {
	repo := infraupload.New(testDB)
	uploadDir := t.TempDir()
	// 小分片仅用于让真实图片被切成多片，生产默认仍为 8MiB。
	handler := httpupload.New(uploadDir, httpupload.WithMultipartRepository(repo), httpupload.WithChunkSize(16))
	engine := integrationMultipartEngine(t, handler)

	content := integrationPNG(t)
	initRecorder := integrationJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": "cover.png",
		"kind":     "cover",
		"size":     len(content),
	})
	if initRecorder.Code != http.StatusCreated {
		t.Fatalf("init 期望 201，实际 %d，body=%s", initRecorder.Code, initRecorder.Body.String())
	}
	_, _, initData := integrationEnvelope(t, initRecorder)
	uploadID, _ := initData["upload_id"].(string)
	totalParts := int(initData["total_parts"].(float64))
	if uploadID == "" || totalParts < 2 {
		t.Fatalf("init 返回不符合预期: %v", initData)
	}

	for number := 1; number <= totalParts; number++ {
		start := (number - 1) * 16
		end := start + 16
		if end > len(content) {
			end = len(content)
		}
		target := fmt.Sprintf("/api/uploads/multipart/%s/parts/%d", uploadID, number)
		req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(content[start:end]))
		req.Header.Set("Content-Type", "application/octet-stream")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("分片 %d 期望 200，实际 %d，body=%s", number, recorder.Code, recorder.Body.String())
		}
	}

	completeRecorder := integrationJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	if completeRecorder.Code != http.StatusCreated {
		t.Fatalf("complete 期望 201，实际 %d，body=%s", completeRecorder.Code, completeRecorder.Body.String())
	}
	_, _, result := integrationEnvelope(t, completeRecorder)
	url, _ := result["url"].(string)
	if !strings.HasPrefix(url, "/uploads/cover/") {
		t.Fatalf("返回 URL 形态不符: %v", result)
	}
	stored, err := os.ReadFile(filepath.Join(uploadDir, "cover", strings.TrimPrefix(url, "/uploads/cover/")))
	if err != nil {
		t.Fatalf("读取成品文件失败: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("合并结果与原文不一致")
	}

	var session infraupload.SessionModel
	if err := testDB.Where("upload_id = ?", uploadID).Take(&session).Error; err != nil {
		t.Fatalf("读取会话行失败: %v", err)
	}
	if session.Status != 2 || session.URL != url {
		t.Fatalf("会话状态/URL 不符: status=%d url=%s", session.Status, session.URL)
	}
	var partCount int64
	if err := testDB.Model(&infraupload.PartModel{}).Where("upload_id = ?", uploadID).Count(&partCount).Error; err != nil {
		t.Fatalf("统计分片行失败: %v", err)
	}
	if int(partCount) != totalParts {
		t.Fatalf("分片行数期望 %d，实际 %d", totalParts, partCount)
	}

	// 幂等：重复 complete 返回 200 与同一结果，且不改变会话。
	again := integrationJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	if again.Code != http.StatusOK {
		t.Fatalf("重复 complete 期望 200，实际 %d，body=%s", again.Code, again.Body.String())
	}
	_, _, repeated := integrationEnvelope(t, again)
	if repeated["url"] != url {
		t.Fatalf("重复 complete 应返回同一 url，期望 %s，实际 %v", url, repeated["url"])
	}
	if err := testDB.Where("upload_id = ?", uploadID).Take(&session).Error; err != nil {
		t.Fatalf("重复 complete 后读取会话失败: %v", err)
	}
	if session.Status != 2 {
		t.Fatalf("重复 complete 后状态应为 completed，实际 %d", session.Status)
	}
}
