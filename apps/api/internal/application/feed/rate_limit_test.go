// rate_limit_test.go 覆盖 following/hot 的请求级限流与僵尸条目的卡片/计数负占位。
package appfeed_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// fakeFeedLimiter 是可注入放行/拒绝/故障的限流器。
type fakeFeedLimiter struct {
	userAllowed bool
	userRetry   time.Duration
	userErr     error
	ipAllowed   bool
	ipRetry     time.Duration
	ipErr       error

	userCalls  int
	ipCalls    int
	lastUserID int64
	lastIP     string
}

func (l *fakeFeedLimiter) AllowUser(_ context.Context, viewerID int64) (bool, time.Duration, error) {
	l.userCalls++
	l.lastUserID = viewerID
	return l.userAllowed, l.userRetry, l.userErr
}

func (l *fakeFeedLimiter) AllowIP(_ context.Context, clientIP string) (bool, time.Duration, error) {
	l.ipCalls++
	l.lastIP = clientIP
	return l.ipAllowed, l.ipRetry, l.ipErr
}

// fakeHotCache 是最小化的热度窗口实现，供 hot 场景限流用例使用。
type fakeHotCache struct {
	items []domfeed.HotItem
	err   error
}

func (c *fakeHotCache) Enabled() bool { return true }

func (c *fakeHotCache) ListHotWindow(context.Context) ([]domfeed.HotItem, error) {
	return c.items, c.err
}

// TestFollowingRateLimited 验证 following 超限时返回带 RetryAfter 的领域错误且不再回源。
func TestFollowingRateLimited(t *testing.T) {
	ctx := context.Background()
	repo, cache := newFollowingFaultFixture()
	limiter := &fakeFeedLimiter{userAllowed: false, userRetry: 37 * time.Second}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache), appfeed.WithFeedRateLimiter(limiter))

	_, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20})
	var limited *domfeed.ErrFeedRateLimited
	if !errors.As(err, &limited) {
		t.Fatalf("超限应返回 ErrFeedRateLimited，实际 %v", err)
	}
	if limited.RetryAfter != 37*time.Second {
		t.Fatalf("RetryAfter 应透传限流窗口剩余时长，实际 %v", limited.RetryAfter)
	}
	if limiter.userCalls != 1 || limiter.lastUserID != 5 || limiter.ipCalls != 0 {
		t.Fatalf("登录读者应按用户维度计数: userCalls=%d lastUserID=%d ipCalls=%d",
			limiter.userCalls, limiter.lastUserID, limiter.ipCalls)
	}
	if len(repo.cardIDs) != 0 || repo.listCalls != 0 {
		t.Fatalf("限流拒绝后不应回源 DB: cardIDs=%v listCalls=%d", repo.cardIDs, repo.listCalls)
	}
}

// TestFeedRateLimiterFailsOpen 验证 Redis 故障时放行并记录降级指标（与登录限流一致）。
func TestFeedRateLimiterFailsOpen(t *testing.T) {
	ctx := context.Background()
	repo, cache := newFollowingFaultFixture()
	limiter := &fakeFeedLimiter{userAllowed: false, userErr: errors.New("redis down")}
	metrics := &recordingMetrics{}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache),
		appfeed.WithFeedRateLimiter(limiter), appfeed.WithMetrics(metrics))

	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20})
	if err != nil {
		t.Fatalf("限流后端故障应 fail-open: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ClipID != 42 {
		t.Fatalf("fail-open 后应正常返回内容: %+v", result.Items)
	}
	if len(metrics.limiterScopes) != 1 || metrics.limiterScopes[0] != "user" {
		t.Fatalf("应记录一次 user 维度的限流降级指标，实际 %v", metrics.limiterScopes)
	}
}

// TestLatestNotRateLimited 验证 latest 走页缓存，不消耗 following/hot 的限流配额。
func TestLatestNotRateLimited(t *testing.T) {
	ctx := context.Background()
	limiter := &fakeFeedLimiter{ipAllowed: false}
	svc := appfeed.New(newFeedFakeRepo(3), appfeed.WithFeedRateLimiter(limiter))

	if _, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Limit: 2}); err != nil {
		t.Fatalf("latest 不应被限流: %v", err)
	}
	if limiter.userCalls != 0 || limiter.ipCalls != 0 {
		t.Fatalf("latest 不应调用限流器: userCalls=%d ipCalls=%d", limiter.userCalls, limiter.ipCalls)
	}
}

// TestHotAnonymousRateLimitByIP 验证匿名 hot 请求按来源 IP 计数。
func TestHotAnonymousRateLimitByIP(t *testing.T) {
	ctx := context.Background()
	limiter := &fakeFeedLimiter{ipAllowed: false, ipRetry: 10 * time.Second}
	svc := appfeed.New(newFeedFakeRepo(0),
		appfeed.WithHotStrategy(&fakeHotCache{}), appfeed.WithFeedRateLimiter(limiter))

	_, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "hot", ClientIP: "1.2.3.4", Limit: 20})
	var limited *domfeed.ErrFeedRateLimited
	if !errors.As(err, &limited) {
		t.Fatalf("匿名 hot 超限应返回 ErrFeedRateLimited，实际 %v", err)
	}
	if limiter.ipCalls != 1 || limiter.lastIP != "1.2.3.4" || limiter.userCalls != 0 {
		t.Fatalf("匿名 hot 应按 IP 计数: ipCalls=%d lastIP=%q userCalls=%d",
			limiter.ipCalls, limiter.lastIP, limiter.userCalls)
	}
}

// TestAnonymousFollowingSkipsRateLimit 验证未登录 following 仍返回 401，且不按 IP 消耗配额。
func TestAnonymousFollowingSkipsRateLimit(t *testing.T) {
	ctx := context.Background()
	repo, cache := newFollowingFaultFixture()
	limiter := &fakeFeedLimiter{ipAllowed: false}
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache), appfeed.WithFeedRateLimiter(limiter))

	_, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "following", Limit: 20})
	if !errors.Is(err, domfeed.ErrViewerRequired) {
		t.Fatalf("未登录 following 应返回 ErrViewerRequired，实际 %v", err)
	}
	if limiter.userCalls != 0 || limiter.ipCalls != 0 {
		t.Fatalf("未登录 following 不应消耗限流配额: userCalls=%d ipCalls=%d", limiter.userCalls, limiter.ipCalls)
	}
}

// TestFollowingZombieCardNegativePlaceholder 验证已删作品的卡片/计数写负占位后不再重复回源。
func TestFollowingZombieCardNegativePlaceholder(t *testing.T) {
	ctx := context.Background()
	repo, cache := newFollowingFaultFixture()
	zombie := domfeed.InboxEntry{ClipID: 43, CreatorID: 7, PublishedAt: time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)}
	cache.inbox = append(cache.inbox, zombie)
	svc := appfeed.New(repo, appfeed.WithFollowingStrategy(cache))
	request := &appfeed.FeedRequest{Scene: "following", ViewerID: 5, Limit: 20}

	first, err := svc.GetFeed(ctx, request)
	if err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].ClipID != 42 {
		t.Fatalf("僵尸条目不应出现在结果中: %+v", first.Items)
	}
	if !cache.negativeCards[43] {
		t.Fatalf("DB 查不到的 clip 应写卡片负占位，实际 %v", cache.negativeCards)
	}
	if !containsID(repo.cardIDs, 43) {
		t.Fatalf("首次读取应把僵尸 ID 一并回源，实际 %v", repo.cardIDs)
	}

	// 第二次读取：僵尸 ID 命中负占位，卡片的批量回源不应再包含它。
	repo.cardIDs = nil
	repo.statsIDs = nil
	second, err := svc.GetFeed(ctx, request)
	if err != nil {
		t.Fatalf("第二次读取失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ClipID != 42 {
		t.Fatalf("正常内容应继续可见: %+v", second.Items)
	}
	if containsID(repo.cardIDs, 43) {
		t.Fatalf("负占位命中后不应再回源僵尸卡片: %v", repo.cardIDs)
	}
	if containsID(repo.statsIDs, 43) {
		t.Fatalf("负占位命中后不应再回源僵尸计数: %v", repo.statsIDs)
	}
}

// containsID 报告 ids 中是否包含指定 ID。
func containsID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
