// interaction_test.go 用内存 fake 仓储验证互动用例：点赞/收藏重复调用幂等、取消后计数归零、
// 评论同幂等键只落一条并保持计数，以及非法参数在应用层被拒绝。
package appaction_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	appaction "pulsefeed/internal/application/interaction"
	dominteraction "pulsefeed/internal/domain/interaction"
)

// interactionFakeActionKey 是 fake 中行为记录的唯一键。
type interactionFakeActionKey struct {
	userID     int64
	clipID     int64
	actionType dominteraction.ActionType
}

// interactionFake 是内存版互动仓储，按领域端口语义维护行为状态、计数与评论。
// SetAction/ExistsPublished/deleteClip 由 mu 保护，供并发用例安全调用。
type interactionFake struct {
	mu            sync.Mutex
	published     map[int64]int64 // clipID -> 创作者 ID
	actions       map[interactionFakeActionKey]*dominteraction.Action
	likeCounts    map[int64]int64
	saveCounts    map[int64]int64
	comments      []*dominteraction.Comment
	commentByKey  map[string]*dominteraction.Comment
	commentCounts map[int64]int64
	nextCommentID int64
}

// newInteractionFake 构造仅包含指定发布 clip 的仓储。
func newInteractionFake(clipIDs ...int64) *interactionFake {
	clipMap := make(map[int64]int64, len(clipIDs))
	for _, id := range clipIDs {
		clipMap[id] = 1
	}
	return &interactionFake{
		published:     clipMap,
		actions:       map[interactionFakeActionKey]*dominteraction.Action{},
		likeCounts:    map[int64]int64{},
		saveCounts:    map[int64]int64{},
		commentByKey:  map[string]*dominteraction.Comment{},
		commentCounts: map[int64]int64{},
	}
}

// adjust 按行为类型增减计数并保证下限为 0。
func (f *interactionFake) adjust(clipID int64, actionType dominteraction.ActionType, delta int64) {
	counts := f.likeCounts
	if actionType == dominteraction.ActionSave {
		counts = f.saveCounts
	}
	value := counts[clipID] + delta
	if value < 0 {
		value = 0
	}
	counts[clipID] = value
}

// SetAction 内存实现：仅状态翻转时调整计数，重复调用不改变计数。
func (f *interactionFake) SetAction(_ context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, idempotencyKey string) (*dominteraction.ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !actionType.Valid() {
		return nil, dominteraction.ErrInvalidActionType
	}
	if _, ok := f.published[clipID]; !ok {
		return nil, dominteraction.ErrClipNotFound
	}
	desired := dominteraction.StatusCanceled
	if active {
		desired = dominteraction.StatusActive
	}

	key := interactionFakeActionKey{userID: userID, clipID: clipID, actionType: actionType}
	result := &dominteraction.ActionResult{ClipID: clipID, ActionType: actionType, Active: active}
	action, exists := f.actions[key]
	switch {
	case !exists:
		f.actions[key] = &dominteraction.Action{
			UserID:         userID,
			ClipID:         clipID,
			Type:           actionType,
			Status:         desired,
			IdempotencyKey: idempotencyKey,
		}
		if active {
			f.adjust(clipID, actionType, 1)
			result.Changed = true
		}
	case action.Status == desired:
		action.IdempotencyKey = idempotencyKey
	default:
		action.Status = desired
		action.IdempotencyKey = idempotencyKey
		delta := int64(-1)
		if active {
			delta = 1
		}
		f.adjust(clipID, actionType, delta)
		result.Changed = true
	}

	result.LikeCount = f.likeCounts[clipID]
	result.SaveCount = f.saveCounts[clipID]
	return result, nil
}

// ExistsPublished 报告 clip 是否存在于发布集合。
func (f *interactionFake) ExistsPublished(_ context.Context, clipID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.published[clipID]
	return ok, nil
}

// deleteClip 模拟作品删除：从发布集合移除，后续互动按不存在处理。
func (f *interactionFake) deleteClip(clipID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.published, clipID)
}

// CreateComment 内存实现：同 (userID, 幂等键) 命中时返回既有评论且不重复计数。
func (f *interactionFake) CreateComment(_ context.Context, comment *dominteraction.Comment, idempotencyKey string) (*dominteraction.Comment, int64, error) {
	if _, ok := f.published[comment.ClipID]; !ok {
		return nil, 0, dominteraction.ErrClipNotFound
	}
	key := interactionFakeCommentKey(comment.UserID, idempotencyKey)
	if idempotencyKey != "" {
		if existing, ok := f.commentByKey[key]; ok {
			return existing, f.commentCounts[comment.ClipID], nil
		}
	}

	f.nextCommentID++
	comment.ID = f.nextCommentID
	comment.Status = dominteraction.CommentStatusVisible
	f.comments = append(f.comments, comment)
	f.commentCounts[comment.ClipID]++
	if idempotencyKey != "" {
		f.commentByKey[key] = comment
	}
	return comment, f.commentCounts[comment.ClipID], nil
}

// DeleteComment 内存实现：作者或 clip 创作者可删，重复删除幂等。
func (f *interactionFake) DeleteComment(_ context.Context, requesterID, commentID int64) (*dominteraction.DeletedComment, error) {
	var target *dominteraction.Comment
	for _, comment := range f.comments {
		if comment.ID == commentID {
			target = comment
			break
		}
	}
	if target == nil {
		return nil, dominteraction.ErrCommentNotFound
	}
	if target.UserID != requesterID && f.published[target.ClipID] != requesterID {
		return nil, dominteraction.ErrCommentPermissionDenied
	}
	if target.Status != dominteraction.CommentStatusDeleted {
		target.Status = dominteraction.CommentStatusDeleted
		if f.commentCounts[target.ClipID] > 0 {
			f.commentCounts[target.ClipID]--
		}
	}
	return &dominteraction.DeletedComment{
		CommentID:    commentID,
		Status:       target.Status,
		CommentCount: f.commentCounts[target.ClipID],
	}, nil
}

// ListComments 内存实现：按 (created_at, id) 严格递减返回可见评论。
func (f *interactionFake) ListComments(_ context.Context, clipID int64, cursor *dominteraction.CommentCursor, limit int) ([]*dominteraction.Comment, error) {
	visible := make([]*dominteraction.Comment, 0, len(f.comments))
	for _, comment := range f.comments {
		if comment.ClipID == clipID && comment.Status == dominteraction.CommentStatusVisible {
			visible = append(visible, comment)
		}
	}
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].CreatedAt.Equal(visible[j].CreatedAt) {
			return visible[i].ID > visible[j].ID
		}
		return visible[i].CreatedAt.After(visible[j].CreatedAt)
	})

	started := cursor == nil
	page := make([]*dominteraction.Comment, 0, limit)
	for _, comment := range visible {
		if !started {
			if comment.CreatedAt.Before(cursor.CreatedAt) ||
				(comment.CreatedAt.Equal(cursor.CreatedAt) && comment.ID < cursor.CommentID) {
				started = true
			} else {
				continue
			}
		}
		page = append(page, comment)
		if len(page) == limit {
			break
		}
	}
	return page, nil
}

// interactionFakeCommentKey 拼接评论幂等键。
func interactionFakeCommentKey(userID int64, idempotencyKey string) string {
	return strconv.FormatInt(userID, 10) + "|" + idempotencyKey
}

// TestInteractionLikeIsIdempotent 验证连续点赞只计一次，重复调用返回未变化。
func TestInteractionLikeIsIdempotent(t *testing.T) {
	repo := newInteractionFake(11)
	service := appaction.New(repo)
	ctx := context.Background()

	first, err := service.Like(ctx, 7, 11, "like-key-1")
	if err != nil {
		t.Fatalf("首次点赞失败: %v", err)
	}
	if first.LikeCount != 1 || !first.Changed {
		t.Fatalf("首次点赞期望计数 1 且 changed=true，实际 count=%d changed=%v", first.LikeCount, first.Changed)
	}

	second, err := service.Like(ctx, 7, 11, "like-key-2")
	if err != nil {
		t.Fatalf("重复点赞失败: %v", err)
	}
	if second.LikeCount != 1 {
		t.Fatalf("重复点赞后计数应保持 1，实际 %d", second.LikeCount)
	}
	if second.Changed {
		t.Fatalf("重复点赞不应标记 changed")
	}
	if len(repo.actions) != 1 {
		t.Fatalf("同一 (user, clip, 行为) 只应有一行记录，实际 %d", len(repo.actions))
	}
}

// TestInteractionUnlikeResetsCount 验证取消点赞后计数归零且再次点赞可恢复。
func TestInteractionUnlikeResetsCount(t *testing.T) {
	repo := newInteractionFake(11)
	service := appaction.New(repo)
	ctx := context.Background()

	if _, err := service.Like(ctx, 7, 11, ""); err != nil {
		t.Fatalf("点赞失败: %v", err)
	}
	result, err := service.Unlike(ctx, 7, 11, "")
	if err != nil {
		t.Fatalf("取消点赞失败: %v", err)
	}
	if result.LikeCount != 0 {
		t.Fatalf("取消点赞后期望计数 0，实际 %d", result.LikeCount)
	}
	if !result.Changed {
		t.Fatalf("状态翻转应标记 changed")
	}

	// 重复取消：计数保持 0，不再翻转。
	again, err := service.Unlike(ctx, 7, 11, "")
	if err != nil {
		t.Fatalf("重复取消点赞失败: %v", err)
	}
	if again.LikeCount != 0 || again.Changed {
		t.Fatalf("重复取消期望 count=0 changed=false，实际 count=%d changed=%v", again.LikeCount, again.Changed)
	}
}

// TestInteractionSaveIsIdempotentAndUnsave 验证收藏与取消收藏的幂等语义。
func TestInteractionSaveIsIdempotentAndUnsave(t *testing.T) {
	repo := newInteractionFake(12)
	service := appaction.New(repo)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		result, err := service.Save(ctx, 8, 12, "")
		if err != nil {
			t.Fatalf("第 %d 次收藏失败: %v", i+1, err)
		}
		if result.SaveCount != 1 {
			t.Fatalf("第 %d 次收藏后期望计数 1，实际 %d", i+1, result.SaveCount)
		}
	}

	result, err := service.Unsave(ctx, 8, 12, "")
	if err != nil {
		t.Fatalf("取消收藏失败: %v", err)
	}
	if result.SaveCount != 0 {
		t.Fatalf("取消收藏后期望计数 0，实际 %d", result.SaveCount)
	}
	if repo.likeCounts[12] != 0 {
		t.Fatalf("取消收藏不应影响点赞计数，实际 %d", repo.likeCounts[12])
	}
}

// TestInteractionCommentIdempotency 验证同幂等键重复创建评论只落一条且计数正确。
func TestInteractionCommentIdempotency(t *testing.T) {
	repo := newInteractionFake(13)
	service := appaction.New(repo)
	ctx := context.Background()

	first, count, err := service.CreateComment(ctx, 9, 13, "  一条评论  ", "comment-key-1")
	if err != nil {
		t.Fatalf("首次创建评论失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("首次创建后期望评论计数 1，实际 %d", count)
	}
	if first.Content != "一条评论" {
		t.Fatalf("评论内容应去除首尾空白，实际 %q", first.Content)
	}

	second, count, err := service.CreateComment(ctx, 9, 13, "另一条内容", "comment-key-1")
	if err != nil {
		t.Fatalf("重复创建评论失败: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("同幂等键应返回既有评论 id=%d，实际 %d", first.ID, second.ID)
	}
	if count != 1 {
		t.Fatalf("重复创建后计数应保持 1，实际 %d", count)
	}
	if len(repo.comments) != 1 {
		t.Fatalf("同幂等键只应落一条评论，实际 %d", len(repo.comments))
	}
}

// interactionNotifierCall 记录一次通知投递，便于断言触发点与载荷。
type interactionNotifierCall struct {
	kind      string
	actorID   int64
	clipID    int64
	commentID int64
	creatorID int64
	content   string
}

// interactionFakeNotifier 是 MessageNotifier 的内存替身，按调用顺序记录通知。
type interactionFakeNotifier struct {
	calls []interactionNotifierCall
	err   error
}

// NotifyLiked 记录点赞通知。
func (n *interactionFakeNotifier) NotifyLiked(_ context.Context, actorID, clipID, creatorID int64) error {
	n.calls = append(n.calls, interactionNotifierCall{kind: "LIKE", actorID: actorID, clipID: clipID, creatorID: creatorID})
	return n.err
}

// NotifyCommented 记录评论通知。
func (n *interactionFakeNotifier) NotifyCommented(_ context.Context, actorID, clipID, commentID, creatorID int64, content string) error {
	n.calls = append(n.calls, interactionNotifierCall{kind: "COMMENT", actorID: actorID, clipID: clipID, commentID: commentID, creatorID: creatorID, content: content})
	return n.err
}

// ownerResolver 用 fake 仓储的发布关系解析创作者。
func ownerResolver(repo *interactionFake) appaction.ClipOwnerResolver {
	return appaction.ClipOwnerFunc(func(_ context.Context, clipID int64) (int64, error) {
		owner, ok := repo.published[clipID]
		if !ok {
			return 0, dominteraction.ErrClipNotFound
		}
		return owner, nil
	})
}

// TestInteractionNotifiesLikeAndComment 验证点赞仅在状态真正生效时通知，
// 重复点赞与取消点赞不重复通知；评论通知携带评论 ID 与正文，供消息层按 ID 幂等去重。
func TestInteractionNotifiesLikeAndComment(t *testing.T) {
	repo := newInteractionFake(21)
	notifier := &interactionFakeNotifier{}
	service := appaction.New(repo, appaction.WithMessageNotifier(notifier, ownerResolver(repo)))
	ctx := context.Background()

	if _, err := service.Like(ctx, 7, 21, "like-1"); err != nil {
		t.Fatalf("首次点赞失败: %v", err)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("首次点赞应产生 1 条通知，实际 %d", len(notifier.calls))
	}
	call := notifier.calls[0]
	if call.kind != "LIKE" || call.actorID != 7 || call.clipID != 21 || call.creatorID != 1 {
		t.Fatalf("点赞通知载荷 = %+v, 期望 LIKE/7/21/1", call)
	}

	if _, err := service.Like(ctx, 7, 21, "like-2"); err != nil {
		t.Fatalf("重复点赞失败: %v", err)
	}
	if _, err := service.Unlike(ctx, 7, 21, "like-3"); err != nil {
		t.Fatalf("取消点赞失败: %v", err)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("重复点赞/取消点赞不应新增通知，实际 %d", len(notifier.calls))
	}

	first, _, err := service.CreateComment(ctx, 7, 21, "  好片子  ", "comment-1")
	if err != nil {
		t.Fatalf("创建评论失败: %v", err)
	}
	if len(notifier.calls) != 2 {
		t.Fatalf("评论应产生 1 条通知，累计 %d", len(notifier.calls))
	}
	commentCall := notifier.calls[1]
	if commentCall.kind != "COMMENT" || commentCall.commentID != first.ID ||
		commentCall.creatorID != 1 || commentCall.content != "好片子" {
		t.Fatalf("评论通知载荷 = %+v, 期望 COMMENT/评论ID=%d/creator=1/正文=好片子", commentCall, first.ID)
	}

	second, _, err := service.CreateComment(ctx, 7, 21, "另一条", "comment-1")
	if err != nil {
		t.Fatalf("重复创建评论失败: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("同幂等键应复用评论 ID=%d，实际 %d", first.ID, second.ID)
	}
	if len(notifier.calls) != 3 || notifier.calls[2].commentID != first.ID {
		t.Fatalf("重复评论应携带同一评论 ID，实际 calls=%+v", notifier.calls)
	}
}

// TestInteractionSelfActionsDoNotNotify 验证自己给自己的作品点赞/评论不产生通知。
func TestInteractionSelfActionsDoNotNotify(t *testing.T) {
	repo := newInteractionFake(22)
	notifier := &interactionFakeNotifier{}
	service := appaction.New(repo, appaction.WithMessageNotifier(notifier, ownerResolver(repo)))
	ctx := context.Background()

	if _, err := service.Like(ctx, 1, 22, "self-like"); err != nil {
		t.Fatalf("自点赞失败: %v", err)
	}
	if _, _, err := service.CreateComment(ctx, 1, 22, "自评", "self-comment"); err != nil {
		t.Fatalf("自评论失败: %v", err)
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("自己操作自己内容不应产生通知，实际 %d 条", len(notifier.calls))
	}
}

// TestInteractionNotifierFailureDoesNotBreakAction 验证通知失败只影响通知，
// 点赞/评论主链路仍成功返回。
func TestInteractionNotifierFailureDoesNotBreakAction(t *testing.T) {
	repo := newInteractionFake(23)
	notifier := &interactionFakeNotifier{err: errors.New("notify down")}
	service := appaction.New(repo, appaction.WithMessageNotifier(notifier, ownerResolver(repo)))
	ctx := context.Background()

	result, err := service.Like(ctx, 7, 23, "like-1")
	if err != nil {
		t.Fatalf("通知失败不应影响点赞，实际错误: %v", err)
	}
	if result == nil || result.LikeCount != 1 {
		t.Fatalf("点赞结果 = %+v, 期望计数 1", result)
	}
	created, count, err := service.CreateComment(ctx, 7, 23, "评论", "comment-1")
	if err != nil {
		t.Fatalf("通知失败不应影响评论，实际错误: %v", err)
	}
	if created == nil || count != 1 {
		t.Fatalf("评论结果 = %+v count=%d, 期望 1 条", created, count)
	}
}

// TestInteractionRejectsInvalidArguments 验证非法 clip、空评论与越界分页被拒绝。
func TestInteractionRejectsInvalidArguments(t *testing.T) {
	repo := newInteractionFake(14)
	service := appaction.New(repo)
	ctx := context.Background()

	if _, err := service.Like(ctx, 1, 0, ""); !errors.Is(err, dominteraction.ErrClipNotFound) {
		t.Fatalf("clipID 非正期望 ErrClipNotFound，实际 %v", err)
	}
	if _, err := service.Save(ctx, 1, 999, ""); !errors.Is(err, dominteraction.ErrClipNotFound) {
		t.Fatalf("未发布 clip 期望 ErrClipNotFound，实际 %v", err)
	}
	if _, _, err := service.CreateComment(ctx, 1, 14, "   ", ""); !errors.Is(err, dominteraction.ErrInvalidCommentContent) {
		t.Fatalf("空白评论期望 ErrInvalidCommentContent，实际 %v", err)
	}
	if _, err := service.ListComments(ctx, 14, "", -1); !errors.Is(err, dominteraction.ErrInvalidLimit) {
		t.Fatalf("负 limit 期望 ErrInvalidLimit，实际 %v", err)
	}
	if _, err := service.ListComments(ctx, 14, "", dominteraction.MaxListLimit+1); !errors.Is(err, dominteraction.ErrInvalidLimit) {
		t.Fatalf("超上限 limit 期望 ErrInvalidLimit，实际 %v", err)
	}
}

// interactionHotScoreCall 记录一次热度写入，用于断言方向与权重。
type interactionHotScoreCall struct {
	clipID int64
	delta  float64
}

// interactionFakeHotRecorder 是 HotScoreRecorder 的内存替身，按调用顺序记录增量；
// err 非 nil 时模拟 Redis 热度写入失败。
type interactionFakeHotRecorder struct {
	calls []interactionHotScoreCall
	err   error
}

// RecordHotScore 记录一次热度增量，并按配置返回错误。
func (r *interactionFakeHotRecorder) RecordHotScore(_ context.Context, clipID int64, delta float64) error {
	r.calls = append(r.calls, interactionHotScoreCall{clipID: clipID, delta: delta})
	return r.err
}

// interactionFakeStateCache 是 ActionStateCache 的内存替身，返回预设的结果或错误。
type interactionFakeStateCache struct {
	result *dominteraction.ActionResult
	err    error
	calls  int
}

// SetActionState 记录调用次数并返回预设值。
func (c *interactionFakeStateCache) SetActionState(_ context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool) (*dominteraction.ActionResult, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	if c.result != nil {
		return c.result, nil
	}
	return &dominteraction.ActionResult{ClipID: clipID, ActionType: actionType, Active: active, Changed: true, LikeCount: 1}, nil
}

// interactionFakePublisher 是 ActionEventPublisher 的内存替身，记录事件并按配置返回错误。
type interactionFakePublisher struct {
	events []appaction.ActionChanged
	err    error
}

// PublishActionChanged 记录事件并按配置返回错误。
func (p *interactionFakePublisher) PublishActionChanged(_ context.Context, event appaction.ActionChanged) error {
	p.events = append(p.events, event)
	return p.err
}

// interactionFakeMetrics 记录副作用与限流降级指标，键为 "kind:result"。
type interactionFakeMetrics struct {
	sideEffects  []string
	limiterError []string
}

// ObserveInteractionSideEffect 记录一次副作用结果。
func (m *interactionFakeMetrics) ObserveInteractionSideEffect(kind, result string) {
	m.sideEffects = append(m.sideEffects, kind+":"+result)
}

// ObserveInteractionLimiterError 记录一次限流降级。
func (m *interactionFakeMetrics) ObserveInteractionLimiterError(scope string) {
	m.limiterError = append(m.limiterError, scope)
}

// has 报告是否记录过指定结果。
func (m *interactionFakeMetrics) has(want string) bool {
	for _, got := range m.sideEffects {
		if got == want {
			return true
		}
	}
	return false
}

// TestInteractionHotScoreFailureDoesNotBreakAction 验证热度写入失败时点赞/评论仍成功，
// 失败只体现在指标上（提交后副作用不回滚已成功的互动）。
func TestInteractionHotScoreFailureDoesNotBreakAction(t *testing.T) {
	repo := newInteractionFake(25)
	recorder := &interactionFakeHotRecorder{err: errors.New("redis down")}
	metrics := &interactionFakeMetrics{}
	service := appaction.New(repo,
		appaction.WithHotScoreRecorder(recorder),
		appaction.WithMetrics(metrics),
	)
	ctx := context.Background()

	result, err := service.Like(ctx, 7, 25, "hot-fail-like")
	if err != nil {
		t.Fatalf("热度写失败不应影响点赞，实际错误: %v", err)
	}
	if result == nil || result.LikeCount != 1 {
		t.Fatalf("点赞结果 = %+v, 期望计数 1", result)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].delta != 3 {
		t.Fatalf("热度写入调用 = %+v, 期望一次 delta=3", recorder.calls)
	}
	if !metrics.has("hot_score:error") {
		t.Fatalf("热度失败应上报 hot_score:error，实际 %v", metrics.sideEffects)
	}

	if _, count, err := service.CreateComment(ctx, 7, 25, "热度失败也可以评论", "hot-fail-comment"); err != nil || count != 1 {
		t.Fatalf("热度写失败不应影响评论，count=%d err=%v", count, err)
	}
}

// TestInteractionEventPublishFailureFallsBackToSync 验证事件投递失败时回退同步落库，
// 接口响应仍成功，并上报 action_event:error。
func TestInteractionEventPublishFailureFallsBackToSync(t *testing.T) {
	repo := newInteractionFake(26)
	states := &interactionFakeStateCache{}
	publisher := &interactionFakePublisher{err: errors.New("mq down")}
	metrics := &interactionFakeMetrics{}
	service := appaction.New(repo,
		appaction.WithAsyncActionPipeline(states, publisher),
		appaction.WithHotScoreRecorder(&interactionFakeHotRecorder{}),
		appaction.WithMetrics(metrics),
	)
	ctx := context.Background()

	result, err := service.Like(ctx, 7, 26, "event-fail")
	if err != nil {
		t.Fatalf("事件投递失败应回退同步落库，实际错误: %v", err)
	}
	if result == nil || result.LikeCount != 1 || !result.Changed {
		t.Fatalf("点赞结果 = %+v, 期望计数 1 且 changed", result)
	}
	if len(repo.actions) != 1 {
		t.Fatalf("回退后应同步落库 1 行行为，实际 %d", len(repo.actions))
	}
	if !metrics.has("action_event:error") {
		t.Fatalf("事件投递失败应上报 action_event:error，实际 %v", metrics.sideEffects)
	}
}

// TestInteractionAsyncPipelinePublishesEvent 验证异步链路正常时事件按预期投递、
// 请求内不落库；热度由 Worker 在落库后记录，因此 Service 侧不写热度。
func TestInteractionAsyncPipelinePublishesEvent(t *testing.T) {
	repo := newInteractionFake(27)
	states := &interactionFakeStateCache{}
	publisher := &interactionFakePublisher{}
	recorder := &interactionFakeHotRecorder{}
	metrics := &interactionFakeMetrics{}
	service := appaction.New(repo,
		appaction.WithAsyncActionPipeline(states, publisher),
		appaction.WithHotScoreRecorder(recorder),
		appaction.WithMetrics(metrics),
	)
	ctx := context.Background()

	result, err := service.Like(ctx, 7, 27, "async-ok")
	if err != nil {
		t.Fatalf("异步链路点赞失败: %v", err)
	}
	if result == nil || result.LikeCount != 1 {
		t.Fatalf("点赞结果 = %+v, 期望计数 1", result)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("应投递 1 条 action_changed 事件，实际 %d", len(publisher.events))
	}
	event := publisher.events[0]
	if event.UserID != 7 || event.ClipID != 27 || event.ActionType != dominteraction.ActionLike || !event.Active {
		t.Fatalf("事件载荷 = %+v, 期望 user=7 clip=27 like active", event)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("异步链路热度应由 Worker 记录，Service 不应写入，实际 %+v", recorder.calls)
	}
	if len(repo.actions) != 0 {
		t.Fatalf("异步链路请求内不应同步落库，实际 %d 行", len(repo.actions))
	}
	if !metrics.has("action_event:ok") {
		t.Fatalf("事件投递成功应上报 action_event:ok，实际 %v", metrics.sideEffects)
	}
}

// interactionFakeLimiter 是 ActionRateLimiter 的内存替身，返回预设的允许结果或故障。
type interactionFakeLimiter struct {
	allowed    bool
	retryAfter time.Duration
	err        error
	calls      int
	lastUserID int64
}

// AllowUser 记录调用并返回预设结果。
func (l *interactionFakeLimiter) AllowUser(_ context.Context, userID int64) (bool, time.Duration, error) {
	l.calls++
	l.lastUserID = userID
	if l.err != nil {
		return true, 0, l.err
	}
	return l.allowed, l.retryAfter, nil
}

// TestInteractionWriteLimiterRejects 验证点赞/收藏/评论写接口超限返回 ErrActionRateLimited，
// 且不触达仓储；读接口不调用限流器。
func TestInteractionWriteLimiterRejects(t *testing.T) {
	repo := newInteractionFake(31)
	limiter := &interactionFakeLimiter{allowed: false, retryAfter: 3 * time.Second}
	service := appaction.New(repo, appaction.WithRateLimiter(limiter))
	ctx := context.Background()

	var limited *dominteraction.ErrActionRateLimited
	for _, call := range []struct {
		name string
		run  func() error
	}{
		{"点赞", func() error { _, err := service.Like(ctx, 7, 31, ""); return err }},
		{"取消点赞", func() error { _, err := service.Unlike(ctx, 7, 31, ""); return err }},
		{"收藏", func() error { _, err := service.Save(ctx, 7, 31, ""); return err }},
		{"评论", func() error { _, _, err := service.CreateComment(ctx, 7, 31, "内容", ""); return err }},
		{"删除评论", func() error { _, err := service.DeleteComment(ctx, 7, 31); return err }},
	} {
		err := call.run()
		if !errors.As(err, &limited) {
			t.Fatalf("%s 超限期望 ErrActionRateLimited，实际 %v", call.name, err)
		}
		if limited.RetryAfter != 3*time.Second {
			t.Fatalf("%s RetryAfter = %v, 期望 3s", call.name, limited.RetryAfter)
		}
	}
	if len(repo.actions) != 0 || len(repo.comments) != 0 {
		t.Fatalf("超限请求不应触达仓储，actions=%d comments=%d", len(repo.actions), len(repo.comments))
	}

	// 读接口不在此列：分页评论不应消耗或触发限流计数。
	before := limiter.calls
	if _, err := service.ListComments(ctx, 31, "", 10); err != nil {
		t.Fatalf("读取评论失败: %v", err)
	}
	if limiter.calls != before {
		t.Fatalf("读接口不应调用写限流器，调用次数 %d -> %d", before, limiter.calls)
	}
}

// TestInteractionWriteLimiterFailOpen 验证限流器 Redis 故障时放行并上报降级指标。
func TestInteractionWriteLimiterFailOpen(t *testing.T) {
	repo := newInteractionFake(32)
	limiter := &interactionFakeLimiter{err: errors.New("redis down")}
	metrics := &interactionFakeMetrics{}
	service := appaction.New(repo,
		appaction.WithRateLimiter(limiter),
		appaction.WithMetrics(metrics),
	)
	ctx := context.Background()

	result, err := service.Like(ctx, 7, 32, "fail-open")
	if err != nil {
		t.Fatalf("限流器故障应 fail-open，实际错误: %v", err)
	}
	if result == nil || result.LikeCount != 1 {
		t.Fatalf("点赞结果 = %+v, 期望计数 1", result)
	}
	if len(metrics.limiterError) != 1 || metrics.limiterError[0] != "user" {
		t.Fatalf("限流降级指标 = %v, 期望 [user]", metrics.limiterError)
	}
}

// TestInteractionHotScoreFollowsActionDirection 验证热度按行为方向累加：
// 点赞 +3、取消点赞 -3、收藏 +4、取消收藏 -4，重复调用不写入。
func TestInteractionHotScoreFollowsActionDirection(t *testing.T) {
	repo := newInteractionFake(24)
	recorder := &interactionFakeHotRecorder{}
	service := appaction.New(repo, appaction.WithHotScoreRecorder(recorder))
	ctx := context.Background()

	steps := []struct {
		name   string
		call   func() error
		expect float64
		record bool
	}{
		{name: "首次点赞", expect: 3, record: true, call: func() error {
			_, err := service.Like(ctx, 7, 24, "")
			return err
		}},
		{name: "重复点赞", call: func() error {
			_, err := service.Like(ctx, 7, 24, "")
			return err
		}},
		{name: "取消点赞", expect: -3, record: true, call: func() error {
			_, err := service.Unlike(ctx, 7, 24, "")
			return err
		}},
		{name: "重复取消点赞", call: func() error {
			_, err := service.Unlike(ctx, 7, 24, "")
			return err
		}},
		{name: "首次收藏", expect: 4, record: true, call: func() error {
			_, err := service.Save(ctx, 7, 24, "")
			return err
		}},
		{name: "重复收藏", call: func() error {
			_, err := service.Save(ctx, 7, 24, "")
			return err
		}},
		{name: "取消收藏", expect: -4, record: true, call: func() error {
			_, err := service.Unsave(ctx, 7, 24, "")
			return err
		}},
		{name: "重复取消收藏", call: func() error {
			_, err := service.Unsave(ctx, 7, 24, "")
			return err
		}},
	}

	for _, step := range steps {
		if err := step.call(); err != nil {
			t.Fatalf("%s失败: %v", step.name, err)
		}
		if !step.record {
			continue
		}
		last := recorder.calls[len(recorder.calls)-1]
		if last.clipID != 24 || last.delta != step.expect {
			t.Fatalf("%s 热度写入 = %+v, 期望 clip=24 delta=%v", step.name, last, step.expect)
		}
	}

	if len(recorder.calls) != 4 {
		t.Fatalf("仅状态翻转应写入热度，实际 %d 次: %+v", len(recorder.calls), recorder.calls)
	}
	want := []float64{3, -3, 4, -4}
	for i, delta := range want {
		if recorder.calls[i].delta != delta {
			t.Fatalf("第 %d 次热度增量 = %v, 期望 %v", i+1, recorder.calls[i].delta, delta)
		}
	}
}

// interactionActionStep 描述状态机用例中的一步调用与期望结果。
type interactionActionStep struct {
	name        string
	call        func(*appaction.Service) (*dominteraction.ActionResult, error)
	wantCount   int64
	wantChanged bool
}

// TestInteractionIdempotentStateMachine 覆盖取消再点赞、重复点赞、重复取消三条路径：
// 仅状态真正翻转才改变计数，重复操作返回原计数且不标记 changed。
func TestInteractionIdempotentStateMachine(t *testing.T) {
	ctx := context.Background()

	t.Run("取消再点赞", func(t *testing.T) {
		repo := newInteractionFake(41)
		service := appaction.New(repo)
		steps := []interactionActionStep{
			{"首次点赞", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Like(ctx, 3, 41, "a") }, 1, true},
			{"取消点赞", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Unlike(ctx, 3, 41, "b") }, 0, true},
			{"再次点赞", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Like(ctx, 3, 41, "c") }, 1, true},
		}
		assertActionSteps(t, service, repo, steps)
	})

	t.Run("重复点赞", func(t *testing.T) {
		repo := newInteractionFake(42)
		service := appaction.New(repo)
		steps := []interactionActionStep{
			{"首次点赞", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Like(ctx, 3, 42, "a") }, 1, true},
			{"重复点赞", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Like(ctx, 3, 42, "b") }, 1, false},
			{"再次重复点赞", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Like(ctx, 3, 42, "c") }, 1, false},
		}
		assertActionSteps(t, service, repo, steps)
	})

	t.Run("重复取消", func(t *testing.T) {
		repo := newInteractionFake(43)
		service := appaction.New(repo)
		steps := []interactionActionStep{
			{"首次取消", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Unlike(ctx, 3, 43, "a") }, 0, false},
			{"重复取消", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Unlike(ctx, 3, 43, "b") }, 0, false},
			{"再次重复取消", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Unlike(ctx, 3, 43, "c") }, 0, false},
		}
		assertActionSteps(t, service, repo, steps)
	})

	t.Run("收藏取消再收藏", func(t *testing.T) {
		repo := newInteractionFake(44)
		service := appaction.New(repo)
		steps := []interactionActionStep{
			{"首次收藏", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Save(ctx, 3, 44, "a") }, 1, true},
			{"取消收藏", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Unsave(ctx, 3, 44, "b") }, 0, true},
			{"再次收藏", func(s *appaction.Service) (*dominteraction.ActionResult, error) { return s.Save(ctx, 3, 44, "c") }, 1, true},
		}
		assertActionSteps(t, service, repo, steps)
	})
}

// assertActionSteps 逐条执行状态机步骤并断言计数、changed 与行为行数。
func assertActionSteps(t *testing.T, service *appaction.Service, repo *interactionFake, steps []interactionActionStep) {
	t.Helper()
	for _, step := range steps {
		result, err := step.call(service)
		if err != nil {
			t.Fatalf("%s失败: %v", step.name, err)
		}
		gotCount := result.LikeCount
		if result.ActionType == dominteraction.ActionSave {
			gotCount = result.SaveCount
		}
		if gotCount != step.wantCount || result.Changed != step.wantChanged {
			t.Fatalf("%s 结果 count=%d changed=%v，期望 count=%d changed=%v",
				step.name, gotCount, result.Changed, step.wantCount, step.wantChanged)
		}
		repo.mu.Lock()
		rows := len(repo.actions)
		repo.mu.Unlock()
		if rows != 1 {
			t.Fatalf("%s 后同一 (user, clip, 行为) 应只有 1 行，实际 %d", step.name, rows)
		}
	}
}

// TestInteractionConcurrentDuplicateLike 验证同一用户并发重复点赞只计一次，
// 仅一次调用真正翻转状态，其余按幂等命中返回。
func TestInteractionConcurrentDuplicateLike(t *testing.T) {
	repo := newInteractionFake(45)
	service := appaction.New(repo)
	ctx := context.Background()

	const workers = 16
	results := make([]*dominteraction.ActionResult, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = service.Like(ctx, 9, 45, fmt.Sprintf("dup-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()

	changed := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("第 %d 次并发点赞失败: %v", i, errs[i])
		}
		if results[i].Changed {
			changed++
		}
		if results[i].LikeCount < 1 || results[i].LikeCount > workers {
			t.Fatalf("第 %d 次点赞返回计数 %d，超出 [1,%d]", i, results[i].LikeCount, workers)
		}
	}
	if changed != 1 {
		t.Fatalf("并发重复点赞应只有 1 次标记 changed，实际 %d", changed)
	}
	if repo.likeCounts[45] != 1 {
		t.Fatalf("同一用户并发点赞最终计数应为 1，实际 %d", repo.likeCounts[45])
	}
	if len(repo.actions) != 1 {
		t.Fatalf("同一用户并发点赞应只落 1 行行为，实际 %d", len(repo.actions))
	}
}

// TestInteractionConcurrentMultiUserLike 验证多用户并发点赞同一作品时计数不丢。
func TestInteractionConcurrentMultiUserLike(t *testing.T) {
	repo := newInteractionFake(46)
	service := appaction.New(repo)
	ctx := context.Background()

	const users = 32
	errs := make([]error, users)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = service.Like(ctx, int64(i+1), 46, fmt.Sprintf("user-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个用户并发点赞失败: %v", i, err)
		}
	}
	if repo.likeCounts[46] != users {
		t.Fatalf("%d 个用户并发点赞后计数应为 %d，实际 %d", users, users, repo.likeCounts[46])
	}
	if len(repo.actions) != users {
		t.Fatalf("应落 %d 行行为，实际 %d", users, len(repo.actions))
	}
}

// TestInteractionConcurrentLikeWithDelete 验证点赞与作品删除并发后，
// 计数与行为行始终等于成功点赞数，删除后的点赞返回 404 且不再写入。
func TestInteractionConcurrentLikeWithDelete(t *testing.T) {
	repo := newInteractionFake(47)
	service := appaction.New(repo)
	ctx := context.Background()

	const users = 24
	errs := make([]error, users)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = service.Like(ctx, int64(i+1), 47, fmt.Sprintf("race-%d", i))
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		repo.deleteClip(47)
	}()
	close(start)
	wg.Wait()

	accepted := int64(0)
	for i, err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, dominteraction.ErrClipNotFound):
		default:
			t.Fatalf("第 %d 个用户点赞返回非预期错误: %v", i, err)
		}
	}
	if repo.likeCounts[47] != accepted {
		t.Fatalf("最终计数应等于成功点赞数 %d，实际 %d", accepted, repo.likeCounts[47])
	}
	if int64(len(repo.actions)) != accepted {
		t.Fatalf("行为行数应等于成功点赞数 %d，实际 %d", accepted, len(repo.actions))
	}

	// 删除已生效后，新的点赞必须返回 404 且不改变计数。
	if _, err := service.Like(ctx, 999, 47, "after-delete"); !errors.Is(err, dominteraction.ErrClipNotFound) {
		t.Fatalf("删除后点赞期望 ErrClipNotFound，实际 %v", err)
	}
	if repo.likeCounts[47] != accepted {
		t.Fatalf("删除后点赞不应改变计数，期望 %d，实际 %d", accepted, repo.likeCounts[47])
	}
}
