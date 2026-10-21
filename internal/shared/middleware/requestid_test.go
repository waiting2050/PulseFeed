package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDReusesValidHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, RequestIDFrom(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-ID", "client-trace-0001")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if got := recorder.Body.String(); got != "client-trace-0001" {
		t.Fatalf("expected header id reused, got %q", got)
	}
	if got := recorder.Header().Get("X-Request-ID"); got != "client-trace-0001" {
		t.Fatalf("expected response header id, got %q", got)
	}
}

func TestRequestIDRegeneratesInvalidHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, RequestIDFrom(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-ID", "bad id with space")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	generated := recorder.Body.String()
	if generated == "bad id with space" || len(generated) != 32 {
		t.Fatalf("expected regenerated hex id, got %q", generated)
	}
	if strings.ContainsAny(generated, " ") {
		t.Fatalf("expected sanitized id, got %q", generated)
	}
}
