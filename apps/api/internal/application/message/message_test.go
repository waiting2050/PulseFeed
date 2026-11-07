// message_test.go 用内存 fake 验证站内消息的 event_id 幂等创建、参数校验、未读计数、
// 全部标记已读与 (created_at, id) 游标分页的不重不漏。
package appmessage_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	appmessage "pulsefeed/internal/application/message"
	dommessage "pulsefeed/internal/domain/message"
)

// messageFakeRepository 是 message 仓储的内存替身：按 event_id 去重，按时间戳自增模拟落库时间。
type messageFakeRepository struct {
	messages    []*dommessage.Message
	eventIndex  map[string]bool
	nextID      int64
	baseTime    time.Time
	createCalls int
	listCalls   int
	lastCursor  *dommessage.ListCursor
	lastLimit   int
	createErr   error
	listErr     error
	unreadErr   error
	markErr     error
}

var _ dommessage.Repository = (*messageFakeRepository)(nil)

// newMessageFakeRepository 创建内存仓储，并用固定基准时间保证分页顺序确定。
func newMessageFakeRepository() *messageFakeRepository {
	return &messageFakeRepository{
		eventIndex: make(map[string]bool),
		nextID:     1,
		baseTime:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// Create 模拟唯一索引幂等：event_id 已存在时静默忽略并返回 inserted=false。
func (f *messageFakeRepository) Create(_ context.Context, msg *dommessage.Message) (bool, error) {
	f.createCalls++
	if f.createErr != nil {
		return false, f.createErr
	}
	if msg.ReceiverID <= 0 {
		return false, dommessage.ErrInvalidReceiver
	}
	if msg.EventID == "" {
		return false, dommessage.ErrEventIDEmpty
	}
	if f.eventIndex[msg.EventID] {
		return false, nil
	}

	clone := *msg
	clone.ID = f.nextID
	f.nextID++
	clone.CreatedAt = f.baseTime.Add(time.Duration(clone.ID) * time.Minute)
	f.messages = append(f.messages, &clone)
	f.eventIndex[clone.EventID] = true
	return true, nil
}

// ListPage 按 (created_at, id) 严格递减返回接收者消息。
func (f *messageFakeRepository) ListPage(_ context.Context, receiverID int64, cursor *dommessage.ListCursor, limit int) ([]*dommessage.Message, error) {
	f.listCalls++
	f.lastCursor = cursor
	f.lastLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}

	rows := make([]*dommessage.Message, 0, len(f.messages))
	for _, msg := range f.messages {
		if msg.ReceiverID != receiverID {
			continue
		}
		if cursor != nil && !(msg.CreatedAt.Before(cursor.CreatedAt) ||
			(msg.CreatedAt.Equal(cursor.CreatedAt) && msg.ID < cursor.ID)) {
			continue
		}
		rows = append(rows, msg)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// CountUnread 统计接收者未读消息数。
func (f *messageFakeRepository) CountUnread(_ context.Context, receiverID int64) (int64, error) {
	if f.unreadErr != nil {
		return 0, f.unreadErr
	}
	var unread int64
	for _, msg := range f.messages {
		if msg.ReceiverID == receiverID && msg.ReadAt.IsZero() {
			unread++
		}
	}
	return unread, nil
}

// MarkAllRead 把接收者所有未读消息一次标记已读。
func (f *messageFakeRepository) MarkAllRead(_ context.Context, receiverID int64) error {
	if f.markErr != nil {
		return f.markErr
	}
	readAt := f.baseTime.Add(48 * time.Hour)
	for _, msg := range f.messages {
		if msg.ReceiverID == receiverID && msg.ReadAt.IsZero() {
			msg.ReadAt = readAt
		}
	}
	return nil
}

// TestCreateFromActorEventDedupByEventID 验证同一 event_id 第二次创建 inserted=false 且不新增行。
func TestCreateFromActorEventDedupByEventID(t *testing.T) {
	repo := newMessageFakeRepository()
	service := appmessage.New(repo)
	ctx := context.Background()

	inserted, err := service.CreateFromActorEvent(ctx, 7, 8, 7, dommessage.KindFollowed, "evt-1")
	if err != nil {
		t.Fatalf("首次创建返回错误: %v", err)
	}
	if !inserted {
		t.Fatalf("首次创建 inserted = false, 期望 true")
	}

	inserted, err = service.CreateFromActorEvent(ctx, 7, 8, 7, dommessage.KindFollowed, "evt-1")
	if err != nil {
		t.Fatalf("重复创建返回错误: %v", err)
	}
	if inserted {
		t.Fatalf("同一 event_id 第二次创建 inserted = true, 期望 false")
	}

	if len(repo.messages) != 1 {
		t.Fatalf("消息行数 = %d, 期望 1", len(repo.messages))
	}
	if repo.createCalls != 2 {
		t.Fatalf("仓储创建调用次数 = %d, 期望 2", repo.createCalls)
	}

	unread, err := service.CountUnread(ctx, 7)
	if err != nil {
		t.Fatalf("统计未读失败: %v", err)
	}
	if unread != 1 {
		t.Fatalf("未读数 = %d, 期望 1", unread)
	}
}

// TestCreateFromActorEventValidation 验证非法接收者、空 event_id 与未知消息种类在落库前被拒绝。
func TestCreateFromActorEventValidation(t *testing.T) {
	repo := newMessageFakeRepository()
	service := appmessage.New(repo)
	ctx := context.Background()

	cases := []struct {
		name     string
		receiver int64
		actor    int64
		kind     dommessage.Kind
		eventID  string
		want     error
	}{
		{name: "接收者为 0", receiver: 0, actor: 8, kind: dommessage.KindFollowed, eventID: "evt-1", want: dommessage.ErrInvalidReceiver},
		{name: "发送者为负", receiver: 7, actor: -1, kind: dommessage.KindFollowed, eventID: "evt-1", want: dommessage.ErrInvalidReceiver},
		{name: "event_id 为空", receiver: 7, actor: 8, kind: dommessage.KindFollowed, eventID: "", want: dommessage.ErrEventIDEmpty},
		{name: "未知消息种类", receiver: 7, actor: 8, kind: dommessage.Kind("liked"), eventID: "evt-1", want: dommessage.ErrInvalidKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inserted, err := service.CreateFromActorEvent(ctx, tc.receiver, tc.actor, tc.receiver, tc.kind, tc.eventID)
			if inserted {
				t.Fatalf("非法入参不应判定为新增")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("错误 = %v, 期望 %v", err, tc.want)
			}
		})
	}
	if repo.createCalls != 0 {
		t.Fatalf("非法入参不应调用仓储, create_calls=%d", repo.createCalls)
	}
}

// TestNotifyFollowedIsIdempotent 验证关注通知按确定性 event_id 幂等投递且字段完整。
func TestNotifyFollowedIsIdempotent(t *testing.T) {
	repo := newMessageFakeRepository()
	service := appmessage.New(repo)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := service.NotifyFollowed(ctx, 8, 7); err != nil {
			t.Fatalf("第 %d 次关注通知返回错误: %v", i+1, err)
		}
	}

	if len(repo.messages) != 1 {
		t.Fatalf("关注通知行数 = %d, 期望 1", len(repo.messages))
	}
	msg := repo.messages[0]
	if msg.ReceiverID != 7 || msg.ActorID != 8 || msg.TargetID != 7 {
		t.Fatalf("消息归属 receiver=%d actor=%d target=%d, 期望 7/8/7", msg.ReceiverID, msg.ActorID, msg.TargetID)
	}
	if msg.Kind != dommessage.KindFollowed {
		t.Fatalf("消息种类 = %q, 期望 %q", msg.Kind, dommessage.KindFollowed)
	}
	if msg.EventID == "" {
		t.Fatalf("关注通知缺少确定性 event_id")
	}

	unread, err := service.CountUnread(ctx, 7)
	if err != nil {
		t.Fatalf("统计被关注者未读失败: %v", err)
	}
	if unread != 1 {
		t.Fatalf("被关注者未读数 = %d, 期望 1", unread)
	}

	unread, err = service.CountUnread(ctx, 8)
	if err != nil {
		t.Fatalf("统计关注者未读失败: %v", err)
	}
	if unread != 0 {
		t.Fatalf("关注者不应收到提醒, 未读数 = %d", unread)
	}
}

// TestListPageNoRepeatNoGap 验证游标分页按 (created_at, id) 递减且跨页不重不漏、不混入他人消息。
func TestListPageNoRepeatNoGap(t *testing.T) {
	repo := newMessageFakeRepository()
	service := appmessage.New(repo)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		eventID := fmt.Sprintf("evt-7-%d", i)
		if _, err := service.CreateFromActorEvent(ctx, 7, 100, 7, dommessage.KindFollowed, eventID); err != nil {
			t.Fatalf("创建第 %d 条消息失败: %v", i, err)
		}
	}
	if _, err := service.CreateFromActorEvent(ctx, 8, 200, 8, dommessage.KindFollowed, "evt-8-1"); err != nil {
		t.Fatalf("创建其他接收者消息失败: %v", err)
	}

	var cursorText string
	var gotIDs []int64
	received := make(map[int64]int, 5)
	pages := 0
	for {
		pages++
		if pages > 5 {
			t.Fatalf("翻页未终止，已读取 %d 页", pages)
		}
		result, err := service.ListPage(ctx, 7, cursorText, 2)
		if err != nil {
			t.Fatalf("第 %d 页查询失败: %v", pages, err)
		}
		if repo.lastLimit != 3 {
			t.Fatalf("第 %d 页仓储请求条数 = %d, 期望 size+1=3", pages, repo.lastLimit)
		}
		if len(result.Items) == 0 {
			t.Fatalf("第 %d 页为空", pages)
		}
		for _, msg := range result.Items {
			gotIDs = append(gotIDs, msg.ID)
			received[msg.ID]++
		}
		if !result.HasMore {
			if result.NextCursor != "" {
				t.Fatalf("末页不应返回游标, 实际 %q", result.NextCursor)
			}
			break
		}
		if result.NextCursor == "" {
			t.Fatalf("有更多数据时游标不能为空")
		}
		cursorText = result.NextCursor
	}

	if len(gotIDs) != 5 {
		t.Fatalf("累计消息数 = %d, 期望 5（实际 ID=%v）", len(gotIDs), gotIDs)
	}
	for id, count := range received {
		if count != 1 {
			t.Fatalf("消息 %d 被返回 %d 次, 期望 1", id, count)
		}
	}
	wantOrder := []int64{5, 4, 3, 2, 1}
	for i, wantID := range wantOrder {
		if gotIDs[i] != wantID {
			t.Fatalf("第 %d 条消息 ID = %d, 期望 %d（顺序=%v）", i, gotIDs[i], wantID, gotIDs)
		}
	}
}

// TestListPageDefaultLimit 验证 limit=0 使用默认页大小，并按 size+1 请求判断 has_more。
func TestListPageDefaultLimit(t *testing.T) {
	repo := newMessageFakeRepository()
	service := appmessage.New(repo)
	ctx := context.Background()

	total := dommessage.DefaultLimit + 5
	for i := 1; i <= total; i++ {
		eventID := fmt.Sprintf("evt-default-%d", i)
		if _, err := service.CreateFromActorEvent(ctx, 7, 100, 7, dommessage.KindFollowed, eventID); err != nil {
			t.Fatalf("创建第 %d 条消息失败: %v", i, err)
		}
	}

	result, err := service.ListPage(ctx, 7, "", 0)
	if err != nil {
		t.Fatalf("默认分页查询失败: %v", err)
	}
	if repo.lastLimit != dommessage.DefaultLimit+1 {
		t.Fatalf("仓储请求条数 = %d, 期望 %d", repo.lastLimit, dommessage.DefaultLimit+1)
	}
	if len(result.Items) != dommessage.DefaultLimit {
		t.Fatalf("默认页条目数 = %d, 期望 %d", len(result.Items), dommessage.DefaultLimit)
	}
	if !result.HasMore {
		t.Fatalf("超过默认页大小时应标记 has_more")
	}
	if result.NextCursor == "" {
		t.Fatalf("有更多数据时游标不能为空")
	}
}

// TestListPageValidation 验证接收者、limit 与游标非法时的错误。
func TestListPageValidation(t *testing.T) {
	repo := newMessageFakeRepository()
	service := appmessage.New(repo)
	ctx := context.Background()

	if _, err := service.ListPage(ctx, 0, "", 2); !errors.Is(err, dommessage.ErrInvalidReceiver) {
		t.Fatalf("receiver_id=0 错误 = %v, 期望 %v", err, dommessage.ErrInvalidReceiver)
	}
	if _, err := service.ListPage(ctx, 7, "", -1); !errors.Is(err, dommessage.ErrInvalidLimit) {
		t.Fatalf("limit=-1 错误 = %v, 期望 %v", err, dommessage.ErrInvalidLimit)
	}
	if _, err := service.ListPage(ctx, 7, "", dommessage.MaxLimit+1); !errors.Is(err, dommessage.ErrInvalidLimit) {
		t.Fatalf("limit 超上限错误 = %v, 期望 %v", err, dommessage.ErrInvalidLimit)
	}
	if _, err := service.ListPage(ctx, 7, "%%%", 2); !errors.Is(err, dommessage.ErrCursorInvalid) {
		t.Fatalf("非法游标错误 = %v, 期望 %v", err, dommessage.ErrCursorInvalid)
	}
	if repo.listCalls != 0 {
		t.Fatalf("非法入参不应调用仓储, list_calls=%d", repo.listCalls)
	}
}

// TestCountUnreadAndMarkAllRead 验证未读数只统计目标接收者，标记已读后归零且可重复调用。
func TestCountUnreadAndMarkAllRead(t *testing.T) {
	repo := newMessageFakeRepository()
	service := appmessage.New(repo)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		eventID := fmt.Sprintf("evt-7-%d", i)
		if _, err := service.CreateFromActorEvent(ctx, 7, int64(100+i), 7, dommessage.KindFollowed, eventID); err != nil {
			t.Fatalf("创建接收者 7 的消息失败: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		eventID := fmt.Sprintf("evt-8-%d", i)
		if _, err := service.CreateFromActorEvent(ctx, 8, int64(200+i), 8, dommessage.KindFollowed, eventID); err != nil {
			t.Fatalf("创建接收者 8 的消息失败: %v", err)
		}
	}

	unread, err := service.CountUnread(ctx, 7)
	if err != nil {
		t.Fatalf("统计接收者 7 未读失败: %v", err)
	}
	if unread != 3 {
		t.Fatalf("接收者 7 未读数 = %d, 期望 3", unread)
	}
	unread, err = service.CountUnread(ctx, 8)
	if err != nil {
		t.Fatalf("统计接收者 8 未读失败: %v", err)
	}
	if unread != 2 {
		t.Fatalf("接收者 8 未读数 = %d, 期望 2", unread)
	}

	if err := service.MarkAllRead(ctx, 7); err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	unread, err = service.CountUnread(ctx, 7)
	if err != nil {
		t.Fatalf("标记后统计接收者 7 未读失败: %v", err)
	}
	if unread != 0 {
		t.Fatalf("标记已读后接收者 7 未读数 = %d, 期望 0", unread)
	}
	unread, err = service.CountUnread(ctx, 8)
	if err != nil {
		t.Fatalf("标记后统计接收者 8 未读失败: %v", err)
	}
	if unread != 2 {
		t.Fatalf("标记接收者 7 不应影响接收者 8, 实际未读 %d", unread)
	}

	// 再次标记已读应幂等。
	if err := service.MarkAllRead(ctx, 7); err != nil {
		t.Fatalf("重复标记已读失败: %v", err)
	}
	unread, err = service.CountUnread(ctx, 7)
	if err != nil {
		t.Fatalf("重复标记后统计未读失败: %v", err)
	}
	if unread != 0 {
		t.Fatalf("重复标记后未读数 = %d, 期望 0", unread)
	}
	for _, msg := range repo.messages {
		if msg.ReceiverID == 7 && !msg.IsRead() {
			t.Fatalf("接收者 7 的消息 %d 仍为未读", msg.ID)
		}
	}

	if _, err := service.CountUnread(ctx, 0); !errors.Is(err, dommessage.ErrInvalidReceiver) {
		t.Fatalf("CountUnread receiver_id=0 错误 = %v, 期望 %v", err, dommessage.ErrInvalidReceiver)
	}
	if err := service.MarkAllRead(ctx, 0); !errors.Is(err, dommessage.ErrInvalidReceiver) {
		t.Fatalf("MarkAllRead receiver_id=0 错误 = %v, 期望 %v", err, dommessage.ErrInvalidReceiver)
	}
}

// TestListPagePropagatesRepositoryError 验证仓储查询失败向上返回。
func TestListPagePropagatesRepositoryError(t *testing.T) {
	repo := newMessageFakeRepository()
	repo.listErr = errors.New("仓储不可用")
	service := appmessage.New(repo)

	if _, err := service.ListPage(context.Background(), 7, "", 2); err == nil {
		t.Fatalf("仓储查询失败应返回错误")
	}
}
