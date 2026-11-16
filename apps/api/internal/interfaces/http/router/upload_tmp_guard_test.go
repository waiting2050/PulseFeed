// upload_tmp_guard_test.go 验证半成品上传目录的静态访问被全局中间件拦截为 404，
// 且正常上传产物仍能回读；中间件必须注册在 engine.Static 之前。
package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// guardTEngine 按生产顺序先注册拦截中间件、再挂载静态路由，返回引擎。
func guardTEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

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
	engine.Use(blockUploadTmp())
	engine.Static("/uploads", "./uploads")
	return engine
}

// TestUploadTmpStaticAccessIsBlocked 覆盖正常路径、大小写变体与百分号编码变体。
func TestUploadTmpStaticAccessIsBlocked(t *testing.T) {
	engine := guardTEngine(t)

	partDir := filepath.Join("uploads", ".upload-tmp", "abc")
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(partDir, "000001.part"), []byte("part"), 0o644); err != nil {
		t.Fatalf("写入分片文件失败: %v", err)
	}

	paths := []string{
		"/uploads/.upload-tmp/abc/000001.part",
		"/uploads/.UPLOAD-TMP/abc/000001.part",
		"/uploads/%2eupload-tmp/abc/000001.part",
		"/uploads/.upload-tmp",
		"/uploads//.upload-tmp/abc/000001.part",
	}
	for _, target := range paths {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 期望 404，实际 %d", target, recorder.Code)
		}
	}
}

// TestUploadTmpGuardKeepsNormalStaticFiles 验证正常上传产物不受拦截影响。
func TestUploadTmpGuardKeepsNormalStaticFiles(t *testing.T) {
	engine := guardTEngine(t)

	videoDir := filepath.Join("uploads", "video")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("创建静态目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(videoDir, "clip.mp4"), []byte("abcdefgh"), 0o644); err != nil {
		t.Fatalf("写入静态文件失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/uploads/video/clip.mp4", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("正常静态文件期望 200，实际 %d", recorder.Code)
	}
	if recorder.Body.String() != "abcdefgh" {
		t.Fatalf("响应体不符: %q", recorder.Body.String())
	}
}
