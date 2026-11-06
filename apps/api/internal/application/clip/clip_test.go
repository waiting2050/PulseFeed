// clip_test.go 以内存 fake 仓储覆盖 clip 幂等发布、字段校验、详情可见性、创作者分页与删除鉴权。
package appclip_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	appclip "pulsefeed/internal/application/clip"
	domclip "pulsefeed/internal/domain/clip"
)

// clipFakeRepo 是内存版 clip 仓储：维护幂等键索引、自增 ID 与创作者分页。
type clipFakeRepo struct {
	clips         map[int64]*domclip.Clip
	keys          map[string]int64
	nextID        int64
	createCalls   int
	listCalls     int
	deleteCalls   int
	lastListLimit int
	createErr     error
	listErr       error
}

// newClipFakeRepo 构造空的 clip 仓储。
func newClipFakeRepo() *clipFakeRepo {
	return &clipFakeRepo{
		clips: make(map[int64]*domclip.Clip),
		keys:  make(map[string]int64),
	}
}

func clipFakeKey(creatorID int64, idempotencyKey string) string {
	return fmt.Sprintf("%d|%s", creatorID, idempotencyKey)
}

func (f *clipFakeRepo) Create(_ context.Context, clip *domclip.Clip) error {
	f.createCalls++
	if f.createErr != nil {
		return f.createErr
	}
	key := clipFakeKey(clip.CreatorID, clip.IdempotencyKey)
	if _, exists := f.keys[key]; exists {
		return domclip.ErrClipExists
	}
	f.nextID++
	clip.ID = f.nextID
	stored := *clip
	f.clips[stored.ID] = &stored
	f.keys[key] = stored.ID
	return nil
}

func (f *clipFakeRepo) GetByID(_ context.Context, id int64) (*domclip.Clip, error) {
	clip, ok := f.clips[id]
	if !ok {
		return nil, domclip.ErrClipNotFound
	}
	clone := *clip
	return &clone, nil
}

func (f *clipFakeRepo) GetByIdempotencyKey(_ context.Context, creatorID int64, idempotencyKey string) (*domclip.Clip, error) {
	id, ok := f.keys[clipFakeKey(creatorID, idempotencyKey)]
	if !ok {
		return nil, domclip.ErrClipNotFound
	}
	clip, ok := f.clips[id]
	if !ok {
		return nil, domclip.ErrClipNotFound
	}
	clone := *clip
	return &clone, nil
}

func (f *clipFakeRepo) ListByCreator(_ context.Context, creatorID int64, cursor *domclip.Cursor, limit int) ([]*domclip.Clip, error) {
	f.listCalls++
	f.lastListLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	candidates := make([]*domclip.Clip, 0, len(f.clips))
	for _, clip := range f.clips {
		if clip.CreatorID != creatorID || clip.Status != domclip.StatusPublished {
			continue
		}
		if cursor != nil {
			after := clip.PublishedAt.Before(cursor.PublishedAt) ||
				(clip.PublishedAt.Equal(cursor.PublishedAt) && clip.ID < cursor.ClipID)
			if !after {
				continue
			}
		}
		clone := *clip
		candidates = append(candidates, &clone)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].PublishedAt.Equal(candidates[j].PublishedAt) {
			return candidates[i].ID > candidates[j].ID
		}
		return candidates[i].PublishedAt.After(candidates[j].PublishedAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func (f *clipFakeRepo) Delete(_ context.Context, id int64, _ string) error {
	f.deleteCalls++
	clip, ok := f.clips[id]
	if !ok {
		return domclip.ErrClipNotFound
	}
	clip.Status = domclip.StatusDeleted
	return nil
}

// clipFakePublisher 记录两类事件的投递入参与失败注入。
type clipFakePublisher struct {
	publishedEvents []appclip.ClipPublished
	embeddingEvents []appclip.ClipPublished
	publishedErr    error
	embeddingErr    error
}

func (p *clipFakePublisher) PublishClipPublished(_ context.Context, event appclip.ClipPublished) error {
	p.publishedEvents = append(p.publishedEvents, event)
	return p.publishedErr
}

func (p *clipFakePublisher) PublishClipEmbedding(_ context.Context, event appclip.ClipPublished) error {
	p.embeddingEvents = append(p.embeddingEvents, event)
	return p.embeddingErr
}

// clipFakeSeed 直接写入一条可指定发布时间与状态的 clip，供列表/详情用例构造数据。
func clipFakeSeed(repo *clipFakeRepo, id, creatorID int64, title, mediaURL string, publishedAt time.Time, status int) {
	repo.clips[id] = domclip.RestoreClip(id, creatorID, title, mediaURL, "", 0, status, fmt.Sprintf("seed-%d", id), publishedAt)
	if id > repo.nextID {
		repo.nextID = id
	}
}

// TestClipCreatePublishedPublishesEvents 验证合法发布成功并投递 clip.published / clip.embedding。
func TestClipCreatePublishedPublishesEvents(t *testing.T) {
	ctx := context.Background()
	repo := newClipFakeRepo()
	publisher := &clipFakePublisher{}
	svc := appclip.New(repo, appclip.WithPublisher(publisher))

	clip, err := svc.CreatePublished(ctx, 7, "  新 clip  ", " /media/1.mp4 ", " /cover/1.png ", 0, "key-1")
	if err != nil {
		t.Fatalf("合法发布不应失败: %v", err)
	}
	if clip.ID != 1 || clip.CreatorID != 7 {
		t.Fatalf("发布结果 ID/创作者不正确: %+v", clip)
	}
	if clip.Title != "新 clip" || clip.VideoURL != "/media/1.mp4" || clip.CoverURL != "/cover/1.png" {
		t.Fatalf("发布字段未去空白: %+v", clip)
	}
	if clip.Status != domclip.StatusPublished || clip.IdempotencyKey != "key-1" {
		t.Fatalf("发布状态/幂等键不正确: %+v", clip)
	}
	if clip.PublishedAt.IsZero() {
		t.Fatalf("发布时间不应为零值")
	}
	if repo.createCalls != 1 {
		t.Fatalf("仓储 Create 调用次数 = %d, 期望 1", repo.createCalls)
	}

	if len(publisher.publishedEvents) != 1 || len(publisher.embeddingEvents) != 1 {
		t.Fatalf("事件投递次数不正确: published=%d embedding=%d",
			len(publisher.publishedEvents), len(publisher.embeddingEvents))
	}
	event := publisher.publishedEvents[0]
	if event.ClipID != clip.ID || event.CreatorID != 7 || !event.PublishedAt.Equal(clip.PublishedAt) {
		t.Fatalf("clip.published 载荷不正确: %+v", event)
	}
	if publisher.embeddingEvents[0].ClipID != clip.ID {
		t.Fatalf("clip.embedding 载荷 clip_id 不正确: %+v", publisher.embeddingEvents[0])
	}
}

// TestClipCreatePublishedIsIdempotent 验证同幂等键重复发布直接返回既有记录且不再写库/投递。
func TestClipCreatePublishedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := newClipFakeRepo()
	publisher := &clipFakePublisher{}
	svc := appclip.New(repo, appclip.WithPublisher(publisher))

	first, err := svc.CreatePublished(ctx, 7, "首个标题", "/media/1.mp4", "", 0, "same-key")
	if err != nil {
		t.Fatalf("首次发布不应失败: %v", err)
	}
	second, err := svc.CreatePublished(ctx, 7, "另一个标题", "/media/2.mp4", "", 0, "same-key")
	if err != nil {
		t.Fatalf("重复发布不应失败: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("重复发布返回不同 ID: first=%d second=%d", first.ID, second.ID)
	}
	if repo.createCalls != 1 {
		t.Fatalf("重复发布应只写库一次, 实际 %d 次", repo.createCalls)
	}
	if len(publisher.publishedEvents) != 1 || len(publisher.embeddingEvents) != 1 {
		t.Fatalf("重复发布不应重复投递事件: published=%d embedding=%d",
			len(publisher.publishedEvents), len(publisher.embeddingEvents))
	}
}

// TestClipCreatePublishedRejectsInvalidFields 验证标题、媒体地址与幂等键的边界校验均在写库前拦截。
func TestClipCreatePublishedRejectsInvalidFields(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		title     string
		mediaURL  string
		key       string
		wantError error
	}{
		{name: "空标题", title: "", mediaURL: "/media/1.mp4", key: "k1", wantError: domclip.ErrInvalidTitle},
		{name: "仅空白标题", title: "   ", mediaURL: "/media/1.mp4", key: "k2", wantError: domclip.ErrInvalidTitle},
		{name: "超长标题", title: strings.Repeat("t", 129), mediaURL: "/media/1.mp4", key: "k3", wantError: domclip.ErrInvalidTitle},
		{name: "空媒体地址", title: "标题", mediaURL: "", key: "k4", wantError: domclip.ErrInvalidVideoURL},
		{name: "仅空白媒体地址", title: "标题", mediaURL: "   ", key: "k5", wantError: domclip.ErrInvalidVideoURL},
		{name: "空幂等键", title: "标题", mediaURL: "/media/1.mp4", key: "", wantError: domclip.ErrInvalidIdempotencyKey},
		{name: "仅空白幂等键", title: "标题", mediaURL: "/media/1.mp4", key: "   ", wantError: domclip.ErrInvalidIdempotencyKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newClipFakeRepo()
			publisher := &clipFakePublisher{}
			svc := appclip.New(repo, appclip.WithPublisher(publisher))

			if _, err := svc.CreatePublished(ctx, 7, tt.title, tt.mediaURL, "", 0, tt.key); !errors.Is(err, tt.wantError) {
				t.Fatalf("期望错误 %v，实际 %v", tt.wantError, err)
			}
			if repo.createCalls != 0 {
				t.Fatalf("非法发布不应写库, 实际 %d 次", repo.createCalls)
			}
			if len(publisher.publishedEvents) != 0 || len(publisher.embeddingEvents) != 0 {
				t.Fatalf("非法发布不应投递事件")
			}
		})
	}
}

// TestClipCreatePublishedIgnoresPublisherFailure 验证事件投递失败不改变发布结果（不阻塞主链路）。
func TestClipCreatePublishedIgnoresPublisherFailure(t *testing.T) {
	ctx := context.Background()
	repo := newClipFakeRepo()
	brokerErr := errors.New("mq unavailable")
	publisher := &clipFakePublisher{publishedErr: brokerErr, embeddingErr: brokerErr}
	svc := appclip.New(repo, appclip.WithPublisher(publisher))

	clip, err := svc.CreatePublished(ctx, 7, "标题", "/media/1.mp4", "", 0, "key-1")
	if err != nil {
		t.Fatalf("事件投递失败不应阻塞发布: %v", err)
	}
	if clip == nil || clip.ID != 1 {
		t.Fatalf("发布结果丢失: %+v", clip)
	}
	if len(publisher.publishedEvents) != 1 || len(publisher.embeddingEvents) != 1 {
		t.Fatalf("应尝试投递两类事件: published=%d embedding=%d",
			len(publisher.publishedEvents), len(publisher.embeddingEvents))
	}
}

// TestClipGetRespectsVisibility 验证已发布可见、已删除与不存在统一返回 ErrClipNotFound。
func TestClipGetRespectsVisibility(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	repo := newClipFakeRepo()
	clipFakeSeed(repo, 1, 7, "已发布", "/media/1.mp4", base, domclip.StatusPublished)
	clipFakeSeed(repo, 2, 7, "已删除", "/media/2.mp4", base.Add(time.Hour), domclip.StatusDeleted)
	svc := appclip.New(repo)

	clip, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("读取已发布 clip 不应失败: %v", err)
	}
	if clip.ID != 1 || !clip.Visible() {
		t.Fatalf("读取结果不正确: %+v", clip)
	}

	for _, id := range []int64{2, 99} {
		if _, err := svc.Get(ctx, id); !errors.Is(err, domclip.ErrClipNotFound) {
			t.Fatalf("clip %d 期望 ErrClipNotFound，实际 %v", id, err)
		}
	}
}

// TestClipListByCreatorPaginates 验证游标分页不重不漏，且已删除 clip 不进入创作者列表。
func TestClipListByCreatorPaginates(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	repo := newClipFakeRepo()
	clipFakeSeed(repo, 1, 7, "第一条", "/media/1.mp4", base, domclip.StatusPublished)
	clipFakeSeed(repo, 2, 7, "第二条", "/media/2.mp4", base.Add(time.Hour), domclip.StatusPublished)
	clipFakeSeed(repo, 3, 7, "第三条", "/media/3.mp4", base.Add(2*time.Hour), domclip.StatusPublished)
	clipFakeSeed(repo, 4, 7, "已删除", "/media/4.mp4", base.Add(3*time.Hour), domclip.StatusDeleted)
	svc := appclip.New(repo)

	first, err := svc.ListByCreator(ctx, 7, "", 2)
	if err != nil {
		t.Fatalf("首页列表不应失败: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != 3 || first.Items[1].ID != 2 {
		t.Fatalf("首页条目不正确: %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("首页应标记还有下一页并给出游标: %+v", first)
	}
	if repo.lastListLimit != 3 {
		t.Fatalf("仓储 limit = %d, 期望页大小 +1 = 3", repo.lastListLimit)
	}

	cursor, err := domclip.DecodeCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("下一页游标无法解析: %v", err)
	}
	if cursor == nil || cursor.ClipID != 2 || !cursor.PublishedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("游标锚点不正确: %+v", cursor)
	}

	second, err := svc.ListByCreator(ctx, 7, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("第二页列表不应失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != 1 {
		t.Fatalf("第二页条目不正确: %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("末页不应标记还有下一页: %+v", second)
	}

	seen := map[int64]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] {
			t.Fatalf("跨页出现重复 clip_id: %d", item.ID)
		}
		seen[item.ID] = true
		if item.Status != domclip.StatusPublished {
			t.Fatalf("列表不应包含非可见 clip: %+v", item)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("两页应覆盖全部 3 条可见 clip，实际 %d 条", len(seen))
	}
}

// TestClipListByCreatorValidatesPage 验证页大小越界、非法游标与 limit 默认值。
func TestClipListByCreatorValidatesPage(t *testing.T) {
	ctx := context.Background()

	t.Run("页大小为负", func(t *testing.T) {
		repo := newClipFakeRepo()
		svc := appclip.New(repo)
		if _, err := svc.ListByCreator(ctx, 7, "", -1); !errors.Is(err, domclip.ErrInvalidLimit) {
			t.Fatalf("期望 ErrInvalidLimit，实际 %v", err)
		}
		if repo.listCalls != 0 {
			t.Fatalf("非法页大小不应访问仓储, 实际 %d 次", repo.listCalls)
		}
	})

	t.Run("页大小超上限", func(t *testing.T) {
		repo := newClipFakeRepo()
		svc := appclip.New(repo)
		if _, err := svc.ListByCreator(ctx, 7, "", domclip.MaxListLimit+1); !errors.Is(err, domclip.ErrInvalidLimit) {
			t.Fatalf("期望 ErrInvalidLimit，实际 %v", err)
		}
		if repo.listCalls != 0 {
			t.Fatalf("非法页大小不应访问仓储, 实际 %d 次", repo.listCalls)
		}
	})

	t.Run("非法游标", func(t *testing.T) {
		repo := newClipFakeRepo()
		svc := appclip.New(repo)
		if _, err := svc.ListByCreator(ctx, 7, "!!!not-base64!!!", 2); !errors.Is(err, domclip.ErrInvalidCursor) {
			t.Fatalf("期望 ErrInvalidCursor，实际 %v", err)
		}
		if repo.listCalls != 0 {
			t.Fatalf("非法游标不应访问仓储, 实际 %d 次", repo.listCalls)
		}
	})

	t.Run("零取默认页大小", func(t *testing.T) {
		repo := newClipFakeRepo()
		base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
		clipFakeSeed(repo, 1, 7, "标题", "/media/1.mp4", base, domclip.StatusPublished)
		svc := appclip.New(repo)

		result, err := svc.ListByCreator(ctx, 7, "", 0)
		if err != nil {
			t.Fatalf("默认页大小不应失败: %v", err)
		}
		if len(result.Items) != 1 || result.HasMore {
			t.Fatalf("单条数据不应标记还有下一页: %+v", result)
		}
		if repo.lastListLimit != domclip.DefaultListLimit+1 {
			t.Fatalf("仓储 limit = %d, 期望 %d", repo.lastListLimit, domclip.DefaultListLimit+1)
		}
	})
}

// TestClipDeleteEnforcesOwnership 验证仅创作者可删除、重复删除幂等、不存在返回未找到。
func TestClipDeleteEnforcesOwnership(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	repo := newClipFakeRepo()
	clipFakeSeed(repo, 5, 7, "待删除", "/media/5.mp4", base, domclip.StatusPublished)
	svc := appclip.New(repo)

	if err := svc.Delete(ctx, 7, 5, "del-key"); err != nil {
		t.Fatalf("创作者删除不应失败: %v", err)
	}
	if repo.deleteCalls != 1 {
		t.Fatalf("删除应调用仓储一次, 实际 %d 次", repo.deleteCalls)
	}
	if repo.clips[5].Status != domclip.StatusDeleted {
		t.Fatalf("clip 状态未置为已删除: %+v", repo.clips[5])
	}
	if _, err := svc.Get(ctx, 5); !errors.Is(err, domclip.ErrClipNotFound) {
		t.Fatalf("删除后详情期望 ErrClipNotFound，实际 %v", err)
	}

	if err := svc.Delete(ctx, 8, 5, "other-key"); !errors.Is(err, domclip.ErrNotClipOwner) {
		t.Fatalf("非创作者删除期望 ErrNotClipOwner，实际 %v", err)
	}
	if repo.deleteCalls != 1 {
		t.Fatalf("非创作者删除不应触达仓储, 实际 %d 次", repo.deleteCalls)
	}

	if err := svc.Delete(ctx, 7, 5, "del-key"); err != nil {
		t.Fatalf("重复删除应幂等返回 nil: %v", err)
	}
	if repo.deleteCalls != 2 {
		t.Fatalf("重复删除应再次调用仓储, 实际 %d 次", repo.deleteCalls)
	}

	if err := svc.Delete(ctx, 7, 99, "missing-key"); !errors.Is(err, domclip.ErrClipNotFound) {
		t.Fatalf("删除不存在 clip 期望 ErrClipNotFound，实际 %v", err)
	}
	if repo.deleteCalls != 2 {
		t.Fatalf("不存在 clip 不应触达 Delete, 实际 %d 次", repo.deleteCalls)
	}
}
