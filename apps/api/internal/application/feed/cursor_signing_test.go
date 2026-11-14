// cursor_signing_test.go 覆盖服务层的游标签名校验、旧游标降级与空页负标记。
package appfeed_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// TestServiceSignsAndVerifiesCursor 验证服务出口游标带签名，签名游标可继续翻页。
func TestServiceSignsAndVerifiesCursor(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(5)
	codec := domfeed.NewCursorCodec(strings.Repeat("k", 32))
	svc := appfeed.New(repo, appfeed.WithCursorCodec(codec))

	first, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Limit: 2})
	if err != nil {
		t.Fatalf("首页读取失败: %v", err)
	}
	if !strings.Contains(first.NextCursor, ".") {
		t.Fatalf("首页游标应带签名分隔符，实际 %q", first.NextCursor)
	}

	decoded, legacy, err := codec.Decode(first.NextCursor)
	if err != nil || legacy {
		t.Fatalf("签名游标应通过校验: legacy=%v err=%v", legacy, err)
	}
	if decoded.ClipID != 4 || decoded.Version != domfeed.CursorVersionMilli {
		t.Fatalf("游标锚点不正确: %+v", decoded)
	}

	second, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("签名游标续翻失败: %v", err)
	}
	if len(second.Items) != 2 || second.Items[0].ClipID != 3 {
		t.Fatalf("续翻应从游标之后开始: %+v", second.Items)
	}
}

// TestServiceRejectsTamperedCursor 验证被篡改的游标返回签名错误（HTTP 层映射 400）。
func TestServiceRejectsTamperedCursor(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(5)
	codec := domfeed.NewCursorCodec(strings.Repeat("k", 32))
	svc := appfeed.New(repo, appfeed.WithCursorCodec(codec))

	first, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Limit: 2})
	if err != nil {
		t.Fatalf("首页读取失败: %v", err)
	}
	payload, sig, _ := strings.Cut(first.NextCursor, ".")
	tampered := payload + "x." + sig

	if _, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Cursor: tampered, Limit: 2}); !errors.Is(err, domfeed.ErrInvalidCursorSignature) {
		t.Fatalf("篡改游标应返回 ErrInvalidCursorSignature，实际 %v", err)
	}
}

// TestServiceLegacyUnsignedCursorResetsFirstPage 验证无签名旧游标降级首屏而不是报错。
func TestServiceLegacyUnsignedCursorResetsFirstPage(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(5)
	codec := domfeed.NewCursorCodec(strings.Repeat("k", 32))
	svc := appfeed.New(repo, appfeed.WithCursorCodec(codec))

	legacy := domfeed.EncodeCursor(&domfeed.Cursor{ClipID: 1, Version: domfeed.CursorVersionMilli})
	result, err := svc.GetFeed(ctx, &appfeed.FeedRequest{Scene: "latest", Cursor: legacy, Limit: 2})
	if err != nil {
		t.Fatalf("无签名旧游标不应报错: %v", err)
	}
	if len(result.Items) != 2 || result.Items[0].ClipID != 5 {
		t.Fatalf("旧游标应重置为首屏: %+v", result.Items)
	}
	if !strings.Contains(result.NextCursor, ".") {
		t.Fatalf("重置后返回的游标应带签名，实际 %q", result.NextCursor)
	}
}

// TestEmptyPageWritesNegativeMarker 验证回源空页写负标记，后续相同请求不再回源。
func TestEmptyPageWritesNegativeMarker(t *testing.T) {
	ctx := context.Background()
	repo := newFeedFakeRepo(0)
	cache := newFeedFakeCache(true)
	svc := appfeed.New(repo, appfeed.WithFeedCache(cache))
	request := &appfeed.FeedRequest{Scene: "latest", Limit: 2}

	first, err := svc.GetFeed(ctx, request)
	if err != nil {
		t.Fatalf("空页读取失败: %v", err)
	}
	if len(first.Items) != 0 || first.HasMore {
		t.Fatalf("空页结果不正确: %+v", first)
	}
	if cache.emptyCalls != 1 || cache.setCalls != 0 {
		t.Fatalf("空页应写负标记而不是普通页: empty=%d set=%d", cache.emptyCalls, cache.setCalls)
	}

	second, err := svc.GetFeed(ctx, request)
	if err != nil {
		t.Fatalf("第二次空页读取失败: %v", err)
	}
	if len(second.Items) != 0 {
		t.Fatalf("负标记命中应返回空页: %+v", second.Items)
	}
	if repo.listCalls != 1 {
		t.Fatalf("负标记命中后不应回源，实际回源 %d 次", repo.listCalls)
	}
}
