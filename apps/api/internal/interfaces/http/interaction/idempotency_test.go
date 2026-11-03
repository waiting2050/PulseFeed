// idempotency_test.go 覆盖互动接口的 Idempotency-Key 头解析与错误信封：
// 缺省/空白允许但归一为空串，128 字符边界放行，超长以 400 信封拒绝。
package httpaction

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newHeaderContext 构造只携带指定 Idempotency-Key 头的 gin 上下文。
func newHeaderContext(key string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPut, "/api/clips/1/like", nil)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	c.Request = req
	return c
}

// TestIdempotencyKeyNormalization 覆盖缺省、空白、边界长度与超长四种输入。
func TestIdempotencyKeyNormalization(t *testing.T) {
	t.Run("缺省头为空串", func(t *testing.T) {
		key, ok := idempotencyKey(newHeaderContext(""))
		if !ok || key != "" {
			t.Fatalf("期望空键放行，实际 key=%q ok=%v", key, ok)
		}
	})

	t.Run("两侧空白被裁剪", func(t *testing.T) {
		key, ok := idempotencyKey(newHeaderContext("  batch-42  "))
		if !ok || key != "batch-42" {
			t.Fatalf("期望裁剪后 batch-42，实际 key=%q ok=%v", key, ok)
		}
	})

	t.Run("边界长度 128 放行", func(t *testing.T) {
		key, ok := idempotencyKey(newHeaderContext(strings.Repeat("k", idempotencyKeyMaxLen)))
		if !ok || len(key) != idempotencyKeyMaxLen {
			t.Fatalf("期望 128 长度放行，实际 len=%d ok=%v", len(key), ok)
		}
	})

	t.Run("超长返回 400 信封", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/clips/1/like", nil)
		c.Request.Header.Set("Idempotency-Key", strings.Repeat("k", idempotencyKeyMaxLen+1))

		_, ok := idempotencyKey(c)
		if ok {
			t.Fatalf("超长幂等键不应放行")
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("期望 400，实际 %d", rec.Code)
		}
		var envelope map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("错误信封不是合法 JSON: %v", err)
		}
		if code, _ := envelope["code"].(float64); code != http.StatusBadRequest {
			t.Fatalf("错误信封 code 期望 400，实际 %v", envelope["code"])
		}
		if message, _ := envelope["message"].(string); message == "" {
			t.Fatalf("错误信封 message 不应为空")
		}
		if _, exists := envelope["data"]; exists {
			t.Fatalf("错误信封不应携带 data 字段")
		}
	})
}
