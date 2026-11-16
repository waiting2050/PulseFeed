// rate_limit_test.go 覆盖互动写接口的 429 映射与 fail-open：
// 超限返回 429 + Retry-After，限流器故障放行，未登录仍走原有 401。
package httpaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appaction "pulsefeed/internal/application/interaction"
	dominteraction "pulsefeed/internal/domain/interaction"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// limitTestRepo 是互动仓储的最小替身，记录 SetAction 是否被触达。
type limitTestRepo struct {
	setActionCalls int
}

// SetAction 返回成功结果并计数。
func (r *limitTestRepo) SetAction(_ context.Context, _ int64, clipID int64, actionType dominteraction.ActionType, active bool, _ string) (*dominteraction.ActionResult, error) {
	r.setActionCalls++
	return &dominteraction.ActionResult{ClipID: clipID, ActionType: actionType, Active: active, Changed: true, LikeCount: 1}, nil
}

// ExistsPublished 报告 clip 存在。
func (r *limitTestRepo) ExistsPublished(_ context.Context, _ int64) (bool, error) {
	return true, nil
}

// CreateComment 返回创建后的评论与计数。
func (r *limitTestRepo) CreateComment(_ context.Context, comment *dominteraction.Comment, _ string) (*dominteraction.Comment, int64, error) {
	comment.ID = 1
	return comment, 1, nil
}

// DeleteComment 返回删除结果。
func (r *limitTestRepo) DeleteComment(_ context.Context, commentID, _ int64) (*dominteraction.DeletedComment, error) {
	return &dominteraction.DeletedComment{CommentID: commentID, CommentCount: 0}, nil
}

// ListComments 返回空页。
func (r *limitTestRepo) ListComments(_ context.Context, _ int64, _ *dominteraction.CommentCursor, _ int) ([]*dominteraction.Comment, error) {
	return nil, nil
}

// limitTestLimiter 是互动写限流器替身。
type limitTestLimiter struct {
	allowed    bool
	retryAfter time.Duration
	err        error
	calls      int
}

// AllowUser 记录调用并返回预设结果。
func (l *limitTestLimiter) AllowUser(_ context.Context, _ int64) (bool, time.Duration, error) {
	l.calls++
	if l.err != nil {
		return true, 0, l.err
	}
	return l.allowed, l.retryAfter, nil
}

// newLikeContext 构造带登录身份与 clipId 路径参数的点赞请求上下文。
func newLikeContext(rec *httptest.ResponseRecorder, userID int64) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/clips/11/like", nil)
	c.Params = gin.Params{{Key: "clipId", Value: "11"}}
	if userID > 0 {
		c.Set(middleware.ContextUserIDKey, userID)
	}
	return c
}

// TestInteractionHandlerRateLimited 验证超限时返回 429、Retry-After 与统一错误结构。
func TestInteractionHandlerRateLimited(t *testing.T) {
	rec := httptest.NewRecorder()
	repo := &limitTestRepo{}
	limiter := &limitTestLimiter{allowed: false, retryAfter: 2 * time.Second}
	handler := New(appaction.New(repo, appaction.WithRateLimiter(limiter)))

	handler.Like(newLikeContext(rec, 7))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超限状态码 = %d, 期望 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, 期望 \"2\"", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"code":429`) {
		t.Fatalf("429 错误结构不匹配: %s", body)
	}
	if repo.setActionCalls != 0 {
		t.Fatalf("超限不应触达仓储，实际调用 %d 次", repo.setActionCalls)
	}
}

// TestInteractionHandlerCommentRateLimited 验证评论写接口同样走 429 映射。
func TestInteractionHandlerCommentRateLimited(t *testing.T) {
	rec := httptest.NewRecorder()
	limiter := &limitTestLimiter{allowed: false, retryAfter: time.Second}
	handler := New(appaction.New(&limitTestRepo{}, appaction.WithRateLimiter(limiter)))

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/clips/11/comments", strings.NewReader(`{"content":"hi"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "clipId", Value: "11"}}
	c.Set(middleware.ContextUserIDKey, int64(7))

	handler.CreateComment(c)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("评论超限状态码 = %d, 期望 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, 期望 \"1\"", got)
	}
}

// TestInteractionHandlerLimiterFailOpen 验证限流器故障时放行，点赞仍成功。
func TestInteractionHandlerLimiterFailOpen(t *testing.T) {
	rec := httptest.NewRecorder()
	repo := &limitTestRepo{}
	limiter := &limitTestLimiter{err: context.DeadlineExceeded}
	handler := New(appaction.New(repo, appaction.WithRateLimiter(limiter)))

	handler.Like(newLikeContext(rec, 7))

	if rec.Code != http.StatusOK {
		t.Fatalf("限流器故障应放行，状态码 = %d", rec.Code)
	}
	if repo.setActionCalls != 1 {
		t.Fatalf("fail-open 应继续处理点赞，仓储调用 = %d", repo.setActionCalls)
	}
}

// TestInteractionHandlerUnauthorizedBeforeLimiter 验证未登录仍返回原有 401，且不调用限流器。
func TestInteractionHandlerUnauthorizedBeforeLimiter(t *testing.T) {
	rec := httptest.NewRecorder()
	limiter := &limitTestLimiter{allowed: false, retryAfter: time.Second}
	handler := New(appaction.New(&limitTestRepo{}, appaction.WithRateLimiter(limiter)))

	handler.Like(newLikeContext(rec, 0))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录状态码 = %d, 期望 401", rec.Code)
	}
	if limiter.calls != 0 {
		t.Fatalf("未登录不应调用限流器，实际 %d 次", limiter.calls)
	}
}
