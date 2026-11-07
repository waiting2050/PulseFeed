// interaction_test.go 用内存 fake 仓储验证互动用例：点赞/收藏重复调用幂等、取消后计数归零、
// 评论同幂等键只落一条并保持计数，以及非法参数在应用层被拒绝。
package appaction_test

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"testing"

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
type interactionFake struct {
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
	_, ok := f.published[clipID]
	return ok, nil
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
