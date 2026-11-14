// singleflight_test.go 覆盖 latest 页缓存共享回源的 ctx 解耦、超时语义与结果副本隔离。
package appfeed

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domfeed "pulsefeed/internal/domain/feed"
)

// sfCache 是 singleflight 用例使用的最小页缓存。
type sfCache struct {
	mu      sync.Mutex
	entries map[string][]*domfeed.FeedEntry
	empty   map[string]bool
	setPage int
}

func newSFCache() *sfCache {
	return &sfCache{
		entries: make(map[string][]*domfeed.FeedEntry),
		empty:   make(map[string]bool),
	}
}

func (c *sfCache) Enabled() bool { return true }

func (c *sfCache) GetPage(_ context.Context, digest string) ([]*domfeed.FeedEntry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.empty[digest] {
		return nil, true, nil
	}
	entries, ok := c.entries[digest]
	return entries, ok, nil
}

func (c *sfCache) SetPage(_ context.Context, digest string, entries []*domfeed.FeedEntry, _ bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setPage++
	c.entries[digest] = entries
}

func (c *sfCache) SetPageEmpty(_ context.Context, digest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.empty[digest] = true
}

func (c *sfCache) FeedVersion(context.Context) (string, error) { return "", nil }

// sfStrategy 是可控阻塞的 latest 策略：started 在首次 Load 时通知，unblock 关闭后返回。
type sfStrategy struct {
	started chan struct{}
	unblock chan struct{}
	page    *Page
	loadErr error
	loads   atomic.Int32
}

func newSFStrategy(page *Page) *sfStrategy {
	return &sfStrategy{
		started: make(chan struct{}, 1),
		unblock: make(chan struct{}),
		page:    page,
	}
}

func (s *sfStrategy) Scene() domfeed.Scene { return domfeed.SceneLatest }

func (s *sfStrategy) Cacheable() bool { return true }

func (s *sfStrategy) Load(ctx context.Context, _ *FeedRequest, _ *domfeed.Cursor, _ int) (*Page, error) {
	s.loads.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.unblock:
		return s.page, s.loadErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// newSFService 构造只含 latest 阻塞策略的服务。
func newSFService(cache *sfCache, strategy *sfStrategy, timeout time.Duration) *Service {
	return &Service{
		strategies:        map[domfeed.Scene]Strategy{domfeed.SceneLatest: strategy},
		cache:             cache,
		sharedLoadTimeout: timeout,
	}
}

// TestSharedLoadSurvivesWaiterCancellation 验证首个等待者断连不会取消共享执行，
// 其余等待者仍能拿到同一次回源的结果。
func TestSharedLoadSurvivesWaiterCancellation(t *testing.T) {
	cache := newSFCache()
	strategy := newSFStrategy(&Page{Entries: []*domfeed.FeedEntry{{ClipID: 1}}})
	svc := newSFService(cache, strategy, 2*time.Second)
	request := &FeedRequest{Scene: string(domfeed.SceneLatest), Limit: 1}

	// 两个等待者同时进入，共享一次执行。
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelledResult := make(chan error, 1)
	healthyResult := make(chan error, 1)
	go func() {
		_, err := svc.loadFeedPage(cancelCtx, strategy, request, nil, 2)
		cancelledResult <- err
	}()
	go func() {
		_, err := svc.loadFeedPage(context.Background(), strategy, request, nil, 2)
		healthyResult <- err
	}()

	<-strategy.started
	time.Sleep(50 * time.Millisecond) // 让两个等待者都加入同一次执行
	cancel()

	select {
	case err := <-cancelledResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("被取消的等待者应立即返回 context.Canceled，实际 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("被取消的等待者不应继续阻塞")
	}

	// 共享执行仍在进行，健康等待者不应因他人取消而提前结束。
	select {
	case err := <-healthyResult:
		t.Fatalf("健康等待者不应受他人取消影响: %v", err)
	default:
	}

	close(strategy.unblock)
	select {
	case err := <-healthyResult:
		if err != nil {
			t.Fatalf("健康等待者应成功拿到结果: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("健康等待者应在执行完成后返回")
	}
	if loads := strategy.loads.Load(); loads != 1 {
		t.Fatalf("同一 digest 只应回源一次，实际 %d 次", loads)
	}
}

// TestSharedLoadTimeoutPropagates 验证执行侧超时后所有等待者收到同一个 DeadlineExceeded。
func TestSharedLoadTimeoutPropagates(t *testing.T) {
	cache := newSFCache()
	strategy := newSFStrategy(&Page{})
	svc := newSFService(cache, strategy, 100*time.Millisecond)
	request := &FeedRequest{Scene: string(domfeed.SceneLatest), Limit: 1}

	const waiters = 3
	var wg sync.WaitGroup
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.loadFeedPage(context.Background(), strategy, request, nil, 2)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("执行侧超时后等待者应收到 DeadlineExceeded，实际 %v", err)
		}
	}
	if loads := strategy.loads.Load(); loads != 1 {
		t.Fatalf("超时前同一 digest 只应回源一次，实际 %d 次", loads)
	}
}

// TestSharedPageCopiedPerWaiter 验证每个等待者拿到独立的条目切片，
// 并发就地写入不会共享底层数组（配合 go test -race 覆盖该路径）。
func TestSharedPageCopiedPerWaiter(t *testing.T) {
	cache := newSFCache()
	strategy := newSFStrategy(&Page{Entries: []*domfeed.FeedEntry{{ClipID: 1}, {ClipID: 2}}})
	svc := newSFService(cache, strategy, 2*time.Second)
	request := &FeedRequest{Scene: string(domfeed.SceneLatest), Limit: 2}

	const waiters = 8
	pages := make(chan *Page, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			page, err := svc.loadFeedPage(context.Background(), strategy, request, nil, 3)
			if err != nil {
				t.Errorf("并发读取失败: %v", err)
				return
			}
			pages <- page
		}()
	}
	time.Sleep(50 * time.Millisecond) // 让所有等待者加入同一次执行
	close(strategy.unblock)
	wg.Wait()
	close(pages)

	collected := make([]*Page, 0, waiters)
	for page := range pages {
		collected = append(collected, page)
	}
	if len(collected) != waiters {
		t.Fatalf("应返回 %d 个结果，实际 %d", waiters, len(collected))
	}
	if loads := strategy.loads.Load(); loads != 1 {
		t.Fatalf("并发等待应共享一次回源，实际 %d 次", loads)
	}

	// 每个结果的底层数组必须互不相同。
	seen := make(map[**domfeed.FeedEntry]bool, waiters)
	for _, page := range collected {
		if len(page.Entries) == 0 {
			t.Fatal("结果不应为空")
		}
		if seen[&page.Entries[0]] {
			t.Fatal("不同等待者共享了同一条目切片，assemble 就地写入会产生数据竞争")
		}
		seen[&page.Entries[0]] = true
	}

	// 模拟各请求的 assemble：并发替换各自切片槽位。
	var writers sync.WaitGroup
	for _, page := range collected {
		writers.Add(1)
		go func(p *Page) {
			defer writers.Done()
			for i := range p.Entries {
				clone := *p.Entries[i]
				p.Entries[i] = &clone
			}
		}(page)
	}
	writers.Wait()
}
