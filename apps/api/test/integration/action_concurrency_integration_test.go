//go:build integration

// action_concurrency_integration_test.go 用真实 MySQL 验证点赞/收藏去锁后的并发语义：
// 条件插入 + 唯一索引幂等、pf_clip_stats 原子增减，以及点赞与作品删除并发时的一致性。
// 依赖 setup_integration_test.go 的独立 schema；不依赖 Redis/RabbitMQ。
package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	domclip "pulsefeed/internal/domain/clip"
	dominteraction "pulsefeed/internal/domain/interaction"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraaction "pulsefeed/internal/infra/persistence/interaction"

	"gorm.io/gorm"
)

// seedActionClip 写入一条发布态 clip 并登记清理，返回 clip ID。
func seedActionClip(t *testing.T, creatorID int64, title string) int64 {
	t.Helper()
	clipID := seedClip(t, creatorID, title, time.Now(), domclip.StatusPublished)
	t.Cleanup(func() {
		testDB.Delete(&infraaction.ActionModel{}, "clip_id = ?", clipID)
		testDB.Delete(&infraclip.ClipStatsModel{}, "clip_id = ?", clipID)
		testDB.Delete(&infraclip.ClipModel{}, "id = ?", clipID)
	})
	return clipID
}

// readClipStats 读取 DB 中的点赞/收藏计数；计数行缺失按 0 处理。
func readClipStats(t *testing.T, clipID int64) (int64, int64) {
	t.Helper()
	var stats infraclip.ClipStatsModel
	err := testDB.Take(&stats, "clip_id = ?", clipID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0
	}
	if err != nil {
		t.Fatalf("读取 pf_clip_stats 失败: %v", err)
	}
	return stats.LikeCount, stats.SaveCount
}

// countActions 统计某 clip 的行为行数。
func countActions(t *testing.T, clipID int64) int64 {
	t.Helper()
	var total int64
	if err := testDB.Model(&infraaction.ActionModel{}).Where("clip_id = ?", clipID).Count(&total).Error; err != nil {
		t.Fatalf("统计 pf_actions 失败: %v", err)
	}
	return total
}

// TestActionConcurrentSameUserLike 验证同一用户并发重复点赞只计一次、只落一行行为。
func TestActionConcurrentSameUserLike(t *testing.T) {
	ctx := context.Background()
	clipID := seedActionClip(t, 9301, "it-action-same-user")
	repo := infraaction.New(testDB)

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
			results[i], errs[i] = repo.SetAction(ctx, 9311, clipID, dominteraction.ActionLike, true,
				uniqueKey(fmt.Sprintf("it-dup-%d", i)))
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
	}
	if changed != 1 {
		t.Fatalf("同一用户并发重复点赞应只有 1 次状态翻转，实际 %d", changed)
	}
	if like, _ := readClipStats(t, clipID); like != 1 {
		t.Fatalf("同一用户并发点赞后 like_count 应为 1，实际 %d", like)
	}
	if rows := countActions(t, clipID); rows != 1 {
		t.Fatalf("同一用户并发点赞应只落 1 行行为，实际 %d", rows)
	}
}

// TestActionConcurrentMultiUserLike 验证多用户并发点赞同一作品时计数不丢、不少写。
func TestActionConcurrentMultiUserLike(t *testing.T) {
	ctx := context.Background()
	clipID := seedActionClip(t, 9302, "it-action-multi-user")
	repo := infraaction.New(testDB)

	const users = 50
	errs := make([]error, users)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = repo.SetAction(ctx, int64(9400+i), clipID, dominteraction.ActionLike, true,
				uniqueKey(fmt.Sprintf("it-multi-%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个用户并发点赞失败: %v", i, err)
		}
	}
	if like, _ := readClipStats(t, clipID); like != users {
		t.Fatalf("%d 个用户并发点赞后 like_count 应为 %d，实际 %d", users, users, like)
	}
	if rows := countActions(t, clipID); rows != users {
		t.Fatalf("应落 %d 行行为，实际 %d", users, rows)
	}
}

// TestActionLikeWithClipDelete 验证删除与点赞的两种顺序：
// 删除先提交时点赞返回 404 且不写行为/计数；并发竞争时最终计数与行为行等于成功点赞数。
func TestActionLikeWithClipDelete(t *testing.T) {
	repo := infraaction.New(testDB)

	t.Run("删除先提交", func(t *testing.T) {
		ctx := context.Background()
		clipID := seedActionClip(t, 9501, "it-action-delete-first")
		if err := testDB.Model(&infraclip.ClipModel{}).Where("id = ?", clipID).
			Update("status", domclip.StatusDeleted).Error; err != nil {
			t.Fatalf("删除 clip 失败: %v", err)
		}

		if _, err := repo.SetAction(ctx, 9511, clipID, dominteraction.ActionLike, true,
			uniqueKey("it-delete-first")); !errors.Is(err, dominteraction.ErrClipNotFound) {
			t.Fatalf("非发布态点赞期望 ErrClipNotFound，实际 %v", err)
		}
		if like, save := readClipStats(t, clipID); like != 0 || save != 0 {
			t.Fatalf("删除后点赞不应写计数，实际 like=%d save=%d", like, save)
		}
		if rows := countActions(t, clipID); rows != 0 {
			t.Fatalf("删除后点赞不应写行为行，实际 %d", rows)
		}
	})

	t.Run("并发竞争", func(t *testing.T) {
		ctx := context.Background()
		clipID := seedActionClip(t, 9502, "it-action-delete-race")

		const users = 20
		errs := make([]error, users)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < users; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, errs[i] = repo.SetAction(ctx, int64(9600+i), clipID, dominteraction.ActionLike, true,
					uniqueKey(fmt.Sprintf("it-race-%d", i)))
			}(i)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := testDB.Model(&infraclip.ClipModel{}).Where("id = ?", clipID).
				Update("status", domclip.StatusDeleted).Error; err != nil {
				t.Errorf("并发删除 clip 失败: %v", err)
			}
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
		if like, _ := readClipStats(t, clipID); like != accepted {
			t.Fatalf("最终 like_count 应等于成功点赞数 %d，实际 %d", accepted, like)
		}
		if rows := countActions(t, clipID); rows != accepted {
			t.Fatalf("行为行数应等于成功点赞数 %d，实际 %d", accepted, rows)
		}

		// 删除已提交后的新点赞必须返回 404，且不改动计数。
		if _, err := repo.SetAction(ctx, 9700, clipID, dominteraction.ActionLike, true,
			uniqueKey("it-after-delete")); !errors.Is(err, dominteraction.ErrClipNotFound) {
			t.Fatalf("删除后点赞期望 ErrClipNotFound，实际 %v", err)
		}
		if like, _ := readClipStats(t, clipID); like != accepted {
			t.Fatalf("删除后点赞不应改变计数，期望 %d，实际 %d", accepted, like)
		}
	})
}

// TestActionIdempotentPathsOnDB 在真实 MySQL 上覆盖取消再点赞、重复点赞、重复取消三条路径，
// 并确认计数从已有基线出发按方向原子增减。
func TestActionIdempotentPathsOnDB(t *testing.T) {
	ctx := context.Background()
	clipID := seedActionClip(t, 9303, "it-action-state-machine")
	if err := testDB.Create(&infraclip.ClipStatsModel{ClipID: clipID, LikeCount: 10}).Error; err != nil {
		t.Fatalf("写入计数基线失败: %v", err)
	}
	repo := infraaction.New(testDB)
	const userID int64 = 9313

	steps := []struct {
		name        string
		active      bool
		wantLike    int64
		wantChanged bool
	}{
		{"基线后点赞", true, 11, true},
		{"重复点赞", true, 11, false},
		{"取消点赞", false, 10, true},
		{"重复取消", false, 10, false},
		{"取消再点赞", true, 11, true},
	}
	for _, step := range steps {
		result, err := repo.SetAction(ctx, userID, clipID, dominteraction.ActionLike, step.active,
			uniqueKey(step.name))
		if err != nil {
			t.Fatalf("%s失败: %v", step.name, err)
		}
		if result.LikeCount != step.wantLike || result.Changed != step.wantChanged {
			t.Fatalf("%s 返回 like_count=%d changed=%v，期望 like_count=%d changed=%v",
				step.name, result.LikeCount, result.Changed, step.wantLike, step.wantChanged)
		}
		if like, _ := readClipStats(t, clipID); like != step.wantLike {
			t.Fatalf("%s 后 DB like_count=%d，期望 %d", step.name, like, step.wantLike)
		}
	}
	if rows := countActions(t, clipID); rows != 1 {
		t.Fatalf("同一 (user, clip, 行为) 应只有 1 行，实际 %d", rows)
	}

	// 收藏路径复用同一状态机：收藏 +1、重复收藏不变、取消收藏 -1。
	for _, step := range []struct {
		name     string
		active   bool
		wantSave int64
	}{
		{"收藏", true, 1},
		{"重复收藏", true, 1},
		{"取消收藏", false, 0},
	} {
		result, err := repo.SetAction(ctx, userID, clipID, dominteraction.ActionSave, step.active,
			uniqueKey(step.name))
		if err != nil {
			t.Fatalf("%s失败: %v", step.name, err)
		}
		if result.SaveCount != step.wantSave {
			t.Fatalf("%s 返回 save_count=%d，期望 %d", step.name, result.SaveCount, step.wantSave)
		}
	}
}
