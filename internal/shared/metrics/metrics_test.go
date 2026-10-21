package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExpositionContainsHTTPMetrics(t *testing.T) {
	registry := New("test", "dev")
	registry.ObserveRequest(http.MethodGet, "/api/feed-items", 200, 0.01)
	registry.ObserveRequest(http.MethodGet, "/api/feed-items", 500, 0.5)
	registry.ObserveRateLimited("/api/feed-items")
	registry.SetDependency("mysql", true)
	registry.SetDependency("redis", false)

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}

	body := recorder.Body.String()
	wants := []string{
		`pulsefeed_http_requests_total{method="GET",route="/api/feed-items",status="200"} 1`,
		`pulsefeed_http_requests_total{method="GET",route="/api/feed-items",status="500"} 1`,
		`pulsefeed_http_rate_limited_total{route="/api/feed-items"} 1`,
		`pulsefeed_dependency_up{name="mysql"} 1`,
		`pulsefeed_dependency_up{name="redis"} 0`,
		`pulsefeed_build_info{env="dev",version="test"} 1`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("metric %q not found in exposition:\n%s", want, body)
		}
	}
}
