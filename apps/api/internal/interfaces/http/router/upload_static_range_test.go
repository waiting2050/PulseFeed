// upload_static_range_test.go 验证上传产物静态回读路由：与 router.Register 一致的
// engine.Static("/uploads", "./uploads") 注册方式下，Range 请求返回 206 局部内容，缺失文件返回 404。
package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// rangeTEngine 在临时工作目录下按生产注册方式挂载静态路由，返回引擎与临时根目录。
func rangeTEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	// 生产静态目录为相对路径 ./uploads，故切换到临时工作目录以等价复现注册方式。
	root := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前工作目录失败: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("切换到临时目录失败: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("恢复工作目录失败: %v", err)
		}
	})

	engine := gin.New()
	// 与 router.Register 中的静态路由注册保持一致。
	engine.Static("/uploads", "./uploads")
	return engine
}

// TestUploadStaticRangeServesPartialContent 验证 Range: bytes=0-3 返回 206 与 4 字节局部内容。
func TestUploadStaticRangeServesPartialContent(t *testing.T) {
	engine := rangeTEngine(t)

	const content = "abcdefgh"
	dir := filepath.Join("uploads", "video")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建静态目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入静态文件失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/video/clip.mp4", nil)
	req.Header.Set("Range", "bytes=0-3")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求期望 206，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	if got, want := recorder.Header().Get("Content-Range"), "bytes 0-3/8"; got != want {
		t.Fatalf("Content-Range 期望 %q，实际 %q", want, got)
	}
	body := recorder.Body.String()
	if len(body) != 4 {
		t.Fatalf("响应体期望 4 字节，实际 %d 字节: %q", len(body), body)
	}
	if body != content[:4] {
		t.Fatalf("响应体期望 %q，实际 %q", content[:4], body)
	}
}

// TestUploadStaticMissingFileReturnsNotFound 验证不存在的上传产物返回 404。
func TestUploadStaticMissingFileReturnsNotFound(t *testing.T) {
	engine := rangeTEngine(t)
	if err := os.MkdirAll(filepath.Join("uploads", "video"), 0o755); err != nil {
		t.Fatalf("创建静态目录失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/uploads/video/missing.mp4", nil)
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("缺失文件期望 404，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
}
