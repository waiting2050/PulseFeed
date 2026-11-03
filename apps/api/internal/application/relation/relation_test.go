// relation_test.go 以内存 fake 覆盖关注/取关的幂等状态机、副作用触发条件与关注/粉丝列表分页。
package apprelation_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	apprelation "pulsefeed/internal/application/relation"
	domrelation "pulsefeed/internal/domain/relation"
)

// relationFakeRepo 是内存版关注仓储：维护双向计数、状态迁移、游标分页与错误注入。
type relationFakeRepo struct {
	follows            map[[2]int64]*domrelation.Follow
	followingCounts    map[int64]int64
	followerCounts     map[int64]int64
	missingTargets     map[int64]bool
	setCalls           int
	listFollowingCalls int
	listFollowersCalls int
	lastListLimit      int
	lastIdempotencyKey string
	setErr             error
	listErr            error
	clock              time.Time
}

// newRelationFakeRepo 构造空的关注仓储。
func newRelationFakeRepo() *relationFakeRepo {
	return &relationFakeRepo{
		follows:         make(map[[2]int64]*domrelation.Follow),
		followingCounts: make(map[int64]int64),
		followerCounts:  make(map[int64]int64),
		missingTargets:  make(map[int64]bool),
		clock:           time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
	}
}

// nextTime 返回严格递增的关系更新时间，保证同一测试内的列表顺序确定。
func (f *relationFakeRepo) nextTime() time.Time {
	f.clock = f.clock.Add(time.Minute)
	return f.clock
}

func (f *relationFakeRepo) SetFollow(_ context.Context, userID, targetUserID int64, follow bool, idempotencyKey string) (*domrelation.FollowResult, error) {
	f.setCalls++
	f.lastIdempotencyKey = idempotencyKey
	if f.setErr != nil {
		return nil, f.setErr
	}
	if f.missingTargets[targetUserID] {
		return nil, domrelation.ErrTargetNotFound
	}

	wanted := domrelation.StatusUnfollowed
	if follow {
		wanted = domrelation.StatusFollowing
	}
	key := [2]int64{userID, targetUserID}
	relation := f.follows[key]
	if relation == nil {
		if !follow {
			return &domrelation.FollowResult{
				UserID:         userID,
				TargetUserID:   targetUserID,
				Status:         domrelation.StatusUnfollowed,
				Following:      false,
				FollowingCount: f.followingCounts[userID],
				FollowerCount:  f.followerCounts[targetUserID],
				Changed:        false,
			}, nil
		}
		at := f.nextTime()
		relation = &domrelation.Follow{
			ID:             int64(len(f.follows) + 1),
			UserID:         userID,
			TargetUserID:   targetUserID,
			Status:         wanted,
			IdempotencyKey: idempotencyKey,
			CreatedAt:      at,
			UpdatedAt:      at,
		}
		f.follows[key] = relation
		f.followingCounts[userID]++
		f.followerCounts[targetUserID]++
		return &domrelation.FollowResult{
			UserID:         userID,
			TargetUserID:   targetUserID,
			Status:         wanted,
			Following:      true,
			FollowingCount: f.followingCounts[userID],
			FollowerCount:  f.followerCounts[targetUserID],
			Changed:        true,
		}, nil
	}

	changed := relation.Status != wanted
	if changed {
		relation.Status = wanted
		relation.UpdatedAt = f.nextTime()
		if follow {
			f.followingCounts[userID]++
			f.followerCounts[targetUserID]++
		} else {
			if f.followingCounts[userID] > 0 {
				f.followingCounts[userID]--
			}
			if f.followerCounts[targetUserID] > 0 {
				f.followerCounts[targetUserID]--
			}
		}
	}
	relation.IdempotencyKey = idempotencyKey
	return &domrelation.FollowResult{
		UserID:         userID,
		TargetUserID:   targetUserID,
		Status:         wanted,
		Following:      wanted == domrelation.StatusFollowing,
		FollowingCount: f.followingCounts[userID],
		FollowerCount:  f.followerCounts[targetUserID],
		Changed:        changed,
	}, nil
}

func (f *relationFakeRepo) ListFollowing(_ context.Context, userID int64, cursor *domrelation.ListCursor, limit int) ([]*domrelation.ListEntry, error) {
	f.listFollowingCalls++
	f.lastListLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	entries := make([]*domrelation.ListEntry, 0, len(f.follows))
	for _, relation := range f.follows {
		if relation.UserID != userID || relation.Status != domrelation.StatusFollowing {
			continue
		}
		entries = append(entries, &domrelation.ListEntry{
			UserID:     relation.TargetUserID,
			Nickname:   fmt.Sprintf("user-%d", relation.TargetUserID),
			FollowedAt: relation.UpdatedAt,
		})
	}
	return relationFakePage(entries, cursor, limit), nil
}

func (f *relationFakeRepo) ListFollowers(_ context.Context, userID int64, cursor *domrelation.ListCursor, limit int) ([]*domrelation.ListEntry, error) {
	f.listFollowersCalls++
	f.lastListLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	entries := make([]*domrelation.ListEntry, 0, len(f.follows))
	for _, relation := range f.follows {
		if relation.TargetUserID != userID || relation.Status != domrelation.StatusFollowing {
			continue
		}
		entries = append(entries, &domrelation.ListEntry{
			UserID:     relation.UserID,
			Nickname:   fmt.Sprintf("user-%d", relation.UserID),
			FollowedAt: relation.UpdatedAt,
		})
	}
	return relationFakePage(entries, cursor, limit), nil
}

// relationFakePage 按 (followed_at, user_id) 严格递减排序后套用游标与页大小。
func relationFakePage(entries []*domrelation.ListEntry, cursor *domrelation.ListCursor, limit int) []*domrelation.ListEntry {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].FollowedAt.Equal(entries[j].FollowedAt) {
			return entries[i].UserID > entries[j].UserID
		}
		return entries[i].FollowedAt.After(entries[j].FollowedAt)
	})
	result := make([]*domrelation.ListEntry, 0, limit)
	for _, entry := range entries {
		if cursor != nil {
			after := entry.FollowedAt.Before(cursor.FollowedAt) ||
				(entry.FollowedAt.Equal(cursor.FollowedAt) && entry.UserID < cursor.UserID)
			if !after {
				continue
			}
		}
		result = append(result, entry)
		if len(result) == limit {
			break
		}
	}
	return result
}

// relationFakeSeed 直接写入一条关注中的关系，用于构造确定性的列表数据。
func relationFakeSeed(repo *relationFakeRepo, userID, targetUserID int64, followedAt time.Time) {
	key := [2]int64{userID, targetUserID}
	repo.follows[key] = &domrelation.Follow{
		ID:           int64(len(repo.follows) + 1),
		UserID:       userID,
		TargetUserID: targetUserID,
		Status:       domrelation.StatusFollowing,
		CreatedAt:    followedAt,
		UpdatedAt:    followedAt,
	}
	repo.followingCounts[userID]++
	repo.followerCounts[targetUserID]++
}

// relationFakeBackfiller 记录关注回填调用并支持失败注入。
type relationFakeBackfiller struct {
	calls        int
	lastFollower int64
	lastCreator  int64
	err          error
}

func (f *relationFakeBackfiller) BackfillFollowing(_ context.Context, followerID, creatorID int64) error {
	f.calls++
	f.lastFollower = followerID
	f.lastCreator = creatorID
	return f.err
}

// relationFakeMessenger 记录关注通知调用并支持失败注入。
type relationFakeMessenger struct {
	calls        int
	lastFollower int64
	lastTarget   int64
	err          error
}

func (f *relationFakeMessenger) NotifyFollowed(_ context.Context, followerID, targetUserID int64) error {
	f.calls++
	f.lastFollower = followerID
	f.lastTarget = targetUserID
	return f.err
}

// TestRelationFollowIsIdempotent 验证重复关注不重复累加计数，副作用仅在首次迁移时触发。
func TestRelationFollowIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := newRelationFakeRepo()
	backfiller := &relationFakeBackfiller{}
	messenger := &relationFakeMessenger{}
	svc := apprelation.New(repo,
		apprelation.WithFollowFeedBackfiller(backfiller),
		apprelation.WithFollowEventMessenger(messenger),
	)

	first, err := svc.Follow(ctx, 1, 2, "  follow-key  ")
	if err != nil {
		t.Fatalf("首次关注不应失败: %v", err)
	}
	if !first.Changed || !first.Following || first.Status != domrelation.StatusFollowing {
		t.Fatalf("首次关注回执不正确: %+v", first)
	}
	if first.FollowingCount != 1 || first.FollowerCount != 1 {
		t.Fatalf("首次关注计数不正确: %+v", first)
	}
	if repo.lastIdempotencyKey != "follow-key" {
		t.Fatalf("幂等键应去空白后落库, 实际 %q", repo.lastIdempotencyKey)
	}
	if backfiller.calls != 1 || backfiller.lastFollower != 1 || backfiller.lastCreator != 2 {
		t.Fatalf("首次关注应触发一次回填: %+v", backfiller)
	}
	if messenger.calls != 1 || messenger.lastFollower != 1 || messenger.lastTarget != 2 {
		t.Fatalf("首次关注应发送一次通知: %+v", messenger)
	}

	second, err := svc.Follow(ctx, 1, 2, "follow-key")
	if err != nil {
		t.Fatalf("重复关注不应失败: %v", err)
	}
	if second.Changed {
		t.Fatalf("重复关注不应再次发生状态迁移: %+v", second)
	}
	if second.FollowingCount != 1 || second.FollowerCount != 1 {
		t.Fatalf("重复关注不应累加计数: %+v", second)
	}
	if repo.setCalls != 2 {
		t.Fatalf("仓储 SetFollow 调用次数 = %d, 期望 2", repo.setCalls)
	}
	if backfiller.calls != 1 || messenger.calls != 1 {
		t.Fatalf("幂等命中不应重复触发副作用: backfill=%d notify=%d", backfiller.calls, messenger.calls)
	}
}

// TestRelationUnfollowIsIdempotent 验证取关递减计数一次且重复取关幂等，且不触发回填/通知。
func TestRelationUnfollowIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := newRelationFakeRepo()
	backfiller := &relationFakeBackfiller{}
	messenger := &relationFakeMessenger{}
	svc := apprelation.New(repo,
		apprelation.WithFollowFeedBackfiller(backfiller),
		apprelation.WithFollowEventMessenger(messenger),
	)

	if _, err := svc.Follow(ctx, 1, 2, "follow-key"); err != nil {
		t.Fatalf("关注不应失败: %v", err)
	}
	first, err := svc.Unfollow(ctx, 1, 2, "unfollow-key")
	if err != nil {
		t.Fatalf("首次取关不应失败: %v", err)
	}
	if !first.Changed || first.Following || first.Status != domrelation.StatusUnfollowed {
		t.Fatalf("首次取关回执不正确: %+v", first)
	}
	if first.FollowingCount != 0 || first.FollowerCount != 0 {
		t.Fatalf("取关后计数应归零: %+v", first)
	}

	second, err := svc.Unfollow(ctx, 1, 2, "unfollow-key")
	if err != nil {
		t.Fatalf("重复取关不应失败: %v", err)
	}
	if second.Changed {
		t.Fatalf("重复取关不应再次迁移: %+v", second)
	}
	if second.FollowingCount != 0 || second.FollowerCount != 0 {
		t.Fatalf("重复取关不应产生负计数: %+v", second)
	}
	if backfiller.calls != 1 || messenger.calls != 1 {
		t.Fatalf("取关不应触发回填/通知: backfill=%d notify=%d", backfiller.calls, messenger.calls)
	}
}

// TestRelationFollowRejectsSelf 验证自关注在领域校验阶段被拒，不触达仓储。
func TestRelationFollowRejectsSelf(t *testing.T) {
	ctx := context.Background()
	repo := newRelationFakeRepo()
	svc := apprelation.New(repo)

	if _, err := svc.Follow(ctx, 3, 3, "self-key"); !errors.Is(err, domrelation.ErrFollowSelf) {
		t.Fatalf("自关注期望 ErrFollowSelf，实际 %v", err)
	}
	if repo.setCalls != 0 {
		t.Fatalf("自关注不应访问仓储, 实际 %d 次", repo.setCalls)
	}

	if _, err := svc.Unfollow(ctx, 3, 3, "self-key"); !errors.Is(err, domrelation.ErrFollowSelf) {
		t.Fatalf("自取关期望 ErrFollowSelf，实际 %v", err)
	}
	if repo.setCalls != 0 {
		t.Fatalf("自取关不应访问仓储, 实际 %d 次", repo.setCalls)
	}
}

// TestRelationFollowRejectsInvalidUser 验证用户 ID 非正时返回 ErrTargetNotFound。
func TestRelationFollowRejectsInvalidUser(t *testing.T) {
	ctx := context.Background()
	repo := newRelationFakeRepo()
	svc := apprelation.New(repo)

	tests := []struct {
		name   string
		userID int64
		target int64
	}{
		{name: "操作者为零", userID: 0, target: 2},
		{name: "目标为零", userID: 1, target: 0},
		{name: "操作者为负", userID: -1, target: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Follow(ctx, tt.userID, tt.target, "key"); !errors.Is(err, domrelation.ErrTargetNotFound) {
				t.Fatalf("期望 ErrTargetNotFound，实际 %v", err)
			}
		})
	}
	if repo.setCalls != 0 {
		t.Fatalf("非法用户不应访问仓储, 实际 %d 次", repo.setCalls)
	}
}

// TestRelationFollowTargetNotFound 验证目标用户不存在时透传仓储错误。
func TestRelationFollowTargetNotFound(t *testing.T) {
	ctx := context.Background()
	repo := newRelationFakeRepo()
	repo.missingTargets[2] = true
	svc := apprelation.New(repo)

	result, err := svc.Follow(ctx, 1, 2, "key")
	if !errors.Is(err, domrelation.ErrTargetNotFound) {
		t.Fatalf("期望 ErrTargetNotFound，实际 %v", err)
	}
	if result != nil {
		t.Fatalf("失败时不应返回回执: %+v", result)
	}
}

// TestRelationFollowIgnoresSideEffectFailure 验证回填/通知失败不阻塞关注主链路。
func TestRelationFollowIgnoresSideEffectFailure(t *testing.T) {
	ctx := context.Background()
	repo := newRelationFakeRepo()
	sideErr := errors.New("downstream unavailable")
	backfiller := &relationFakeBackfiller{err: sideErr}
	messenger := &relationFakeMessenger{err: sideErr}
	svc := apprelation.New(repo,
		apprelation.WithFollowFeedBackfiller(backfiller),
		apprelation.WithFollowEventMessenger(messenger),
	)

	result, err := svc.Follow(ctx, 1, 2, "key")
	if err != nil {
		t.Fatalf("副作用失败不应阻塞关注: %v", err)
	}
	if !result.Changed || !result.Following {
		t.Fatalf("关注回执不正确: %+v", result)
	}
	if backfiller.calls != 1 || messenger.calls != 1 {
		t.Fatalf("应各尝试一次副作用: backfill=%d notify=%d", backfiller.calls, messenger.calls)
	}
}

// TestRelationListFollowingPaginates 验证关注列表按游标分页不重不漏。
func TestRelationListFollowingPaginates(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	repo := newRelationFakeRepo()
	relationFakeSeed(repo, 1, 2, base)
	relationFakeSeed(repo, 1, 3, base.Add(time.Minute))
	relationFakeSeed(repo, 1, 4, base.Add(2*time.Minute))
	svc := apprelation.New(repo)

	first, err := svc.ListFollowing(ctx, 1, "", 2)
	if err != nil {
		t.Fatalf("首页关注列表不应失败: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].UserID != 4 || first.Items[1].UserID != 3 {
		t.Fatalf("首页条目不正确: %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("首页应标记还有下一页: %+v", first)
	}
	if repo.lastListLimit != 3 {
		t.Fatalf("仓储 limit = %d, 期望页大小 +1 = 3", repo.lastListLimit)
	}

	cursor, err := domrelation.DecodeListCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("游标无法解析: %v", err)
	}
	if cursor == nil || cursor.UserID != 3 || !cursor.FollowedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("游标锚点不正确: %+v", cursor)
	}

	second, err := svc.ListFollowing(ctx, 1, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("第二页关注列表不应失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].UserID != 2 {
		t.Fatalf("第二页条目不正确: %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("末页不应标记还有下一页: %+v", second)
	}

	seen := map[int64]bool{}
	for _, entry := range append(first.Items, second.Items...) {
		if seen[entry.UserID] {
			t.Fatalf("跨页出现重复用户: %d", entry.UserID)
		}
		seen[entry.UserID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("两页应覆盖全部 3 个关注对象，实际 %d 个", len(seen))
	}
}

// TestRelationListFollowersPaginates 验证粉丝列表复用同一游标分页语义。
func TestRelationListFollowersPaginates(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	repo := newRelationFakeRepo()
	relationFakeSeed(repo, 2, 1, base)
	relationFakeSeed(repo, 3, 1, base.Add(time.Minute))
	relationFakeSeed(repo, 4, 1, base.Add(2*time.Minute))
	svc := apprelation.New(repo)

	first, err := svc.ListFollowers(ctx, 1, "", 2)
	if err != nil {
		t.Fatalf("首页粉丝列表不应失败: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].UserID != 4 || first.Items[1].UserID != 3 {
		t.Fatalf("首页条目不正确: %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("首页应标记还有下一页: %+v", first)
	}
	if repo.lastListLimit != 3 {
		t.Fatalf("仓储 limit = %d, 期望 3", repo.lastListLimit)
	}

	second, err := svc.ListFollowers(ctx, 1, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("第二页粉丝列表不应失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].UserID != 2 || second.HasMore {
		t.Fatalf("第二页条目不正确: %+v", second)
	}
}

// TestRelationListValidatesPage 验证页大小越界、非法游标与 limit 默认值。
func TestRelationListValidatesPage(t *testing.T) {
	ctx := context.Background()

	t.Run("页大小为负", func(t *testing.T) {
		repo := newRelationFakeRepo()
		svc := apprelation.New(repo)
		if _, err := svc.ListFollowing(ctx, 1, "", -1); !errors.Is(err, domrelation.ErrInvalidLimit) {
			t.Fatalf("期望 ErrInvalidLimit，实际 %v", err)
		}
		if repo.listFollowingCalls != 0 {
			t.Fatalf("非法页大小不应访问仓储, 实际 %d 次", repo.listFollowingCalls)
		}
	})

	t.Run("页大小超上限", func(t *testing.T) {
		repo := newRelationFakeRepo()
		svc := apprelation.New(repo)
		if _, err := svc.ListFollowers(ctx, 1, "", domrelation.MaxLimit+1); !errors.Is(err, domrelation.ErrInvalidLimit) {
			t.Fatalf("期望 ErrInvalidLimit，实际 %v", err)
		}
		if repo.listFollowersCalls != 0 {
			t.Fatalf("非法页大小不应访问仓储, 实际 %d 次", repo.listFollowersCalls)
		}
	})

	t.Run("非法游标", func(t *testing.T) {
		repo := newRelationFakeRepo()
		svc := apprelation.New(repo)
		if _, err := svc.ListFollowing(ctx, 1, "!!!not-base64!!!", 2); !errors.Is(err, domrelation.ErrInvalidCursor) {
			t.Fatalf("期望 ErrInvalidCursor，实际 %v", err)
		}
		if repo.listFollowingCalls != 0 {
			t.Fatalf("非法游标不应访问仓储, 实际 %d 次", repo.listFollowingCalls)
		}
	})

	t.Run("零取默认页大小", func(t *testing.T) {
		base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		repo := newRelationFakeRepo()
		relationFakeSeed(repo, 1, 2, base)
		svc := apprelation.New(repo)

		result, err := svc.ListFollowing(ctx, 1, "", 0)
		if err != nil {
			t.Fatalf("默认页大小不应失败: %v", err)
		}
		if len(result.Items) != 1 || result.HasMore {
			t.Fatalf("单条关注不应标记还有下一页: %+v", result)
		}
		if repo.lastListLimit != domrelation.DefaultLimit+1 {
			t.Fatalf("仓储 limit = %d, 期望 %d", repo.lastListLimit, domrelation.DefaultLimit+1)
		}
	})
}
