package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeCounter struct {
	count int64
	err   error
}

func (f *fakeCounter) Incr(_ context.Context, _ string, _ time.Duration) (int64, error) {
	return f.count, f.err
}

func newRateLimitRouter(counter Counter, limit int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID(), RateLimit(RateLimitOptions{
		Counter: counter,
		Limit:   limit,
		Window:  time.Minute,
		Scope:   "test",
	}))
	router.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})
	return router
}

func TestRateLimitAllowsRequestUnderLimit(t *testing.T) {
	router := newRateLimitRouter(&fakeCounter{count: 2}, 3)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ping", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
}

func TestRateLimitRejectsRequestOverLimit(t *testing.T) {
	router := newRateLimitRouter(&fakeCounter{count: 4}, 3)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ping", nil))

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}
}

func TestRateLimitFailsOpenWhenCounterErrors(t *testing.T) {
	router := newRateLimitRouter(&fakeCounter{err: errors.New("redis down")}, 3)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ping", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected fail open, got %d", recorder.Code)
	}
}

func TestRateLimitDisabledWhenLimitIsZero(t *testing.T) {
	router := newRateLimitRouter(nil, 0)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ping", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected disabled limiter to pass, got %d", recorder.Code)
	}
}
