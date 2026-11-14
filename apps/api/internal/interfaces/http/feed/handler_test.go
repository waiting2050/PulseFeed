// handler_test.go 覆盖 Feed 限流错误的传输层映射：429 + Retry-After（秒，向上取整且最小 1）。
package httpfeed

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domfeed "pulsefeed/internal/domain/feed"

	"github.com/gin-gonic/gin"
)

// TestMapErrorFeedRateLimited 验证限流错误映射为 429 并带 Retry-After。
func TestMapErrorFeedRateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		retryAfter time.Duration
		wantHeader string
	}{
		{name: "亚秒向上取整", retryAfter: 200 * time.Millisecond, wantHeader: "1"},
		{name: "毫秒向上取整", retryAfter: 1500 * time.Millisecond, wantHeader: "2"},
		{name: "整秒", retryAfter: 30 * time.Second, wantHeader: "30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)

			mapError(c, &domfeed.ErrFeedRateLimited{RetryAfter: tt.retryAfter})

			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("应返回 429，实际 %d", recorder.Code)
			}
			if got := recorder.Header().Get("Retry-After"); got != tt.wantHeader {
				t.Fatalf("Retry-After 应为 %q，实际 %q", tt.wantHeader, got)
			}
		})
	}
}
