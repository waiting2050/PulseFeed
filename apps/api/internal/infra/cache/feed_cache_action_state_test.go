// feed_cache_action_state_test.go 用进程内 miniredis 验证 WATCH + 16 分片计数：
// 并发点赞/取消后 16 个分片之和与 DB 活跃状态行数一致；同一读者并发重复点赞只计一次；
// 重复取消返回 Changed=false 且计数不越界为负。
package cache

import (
	"context"
	"strconv"
	"sync"
	"testing"

	dominteraction "pulsefeed/internal/domain/interaction"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newActionTestCache 启动进程内 Redis 并返回可用的 FeedCache，测试结束自动回收。
func newActionTestCache(t *testing.T) (*FeedCache, *miniredis.Miniredis) {
	t.Helper()
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return NewFeedCache(client), server
}

// likeShardSums 读取该 clip 的 16 个点赞分片，返回分片值与总和。
func likeShardSums(t *testing.T, cache *FeedCache, clipID int64) ([]int64, int64) {
	t.Helper()
	ctx := context.Background()
	shards := make([]int64, 0, actionShardCount)
	var total int64
	for shard := 0; shard < actionShardCount; shard++ {
		raw, err := cache.client.Get(ctx, actionCountKey(clipID, shard, dominteraction.ActionLike)).Result()
		value := int64(0)
		if err == nil {
			if parsed, convErr := strconv.ParseInt(raw, 10, 64); convErr == nil {
				value = parsed
			}
		} else if err != redis.Nil {
			t.Fatalf("读取分片 %d 计数失败: %v", shard, err)
		}
		shards = append(shards, value)
		total += value
	}
	return shards, total
}

// TestSetActionStateConcurrentLikeUnlikeMatchesDB 模拟 120 位读者并发点赞、60 位取消（其中 20 位重复取消），
// 断言 Redis 分片计数之和始终等于 DB 侧活跃状态行数，且分片计数从不为负。
func TestSetActionStateConcurrentLikeUnlikeMatchesDB(t *testing.T) {
	ctx := context.Background()
	cache, _ := newActionTestCache(t)
	const clipID int64 = 9001
	const likers = 120
	const unlikers = 60

	// dbRows 模拟 pf_actions 活跃状态：只由“真实发生翻转”的调用改动，等价于 Worker 落库后的行数。
	var dbMu sync.Mutex
	dbRows := make(map[int64]bool, likers)
	apply := func(userID int64, result *dominteraction.ActionResult) {
		if result == nil || !result.Changed {
			return
		}
		dbMu.Lock()
		dbRows[userID] = result.Active
		dbMu.Unlock()
	}
	dbActiveCount := func() int {
		dbMu.Lock()
		defer dbMu.Unlock()
		count := 0
		for _, active := range dbRows {
			if active {
				count++
			}
		}
		return count
	}

	// 阶段一：120 位读者并发点赞，最终分片之和与 DB 行数都应为 120。
	runConcurrent(t, likers, func(index int) error {
		userID := int64(index + 1)
		result, err := cache.SetActionState(ctx, userID, clipID, dominteraction.ActionLike, true)
		if err != nil {
			return err
		}
		apply(userID, result)
		return nil
	})

	shards, total := likeShardSums(t, cache, clipID)
	if total != likers {
		t.Fatalf("并发点赞后分片总和 = %d，期望 %d（分片=%v）", total, likers, shards)
	}
	if db := dbActiveCount(); db != int(total) {
		t.Fatalf("分片总和 %d 与 DB 活跃行数 %d 不一致", total, db)
	}
	if countNonZero(shards) < 2 {
		t.Fatalf("120 次点赞只落在 %d 个分片，分片散列未生效: %v", countNonZero(shards), shards)
	}

	// 阶段二：60 位读者并发取消，其中前 20 位再重复取消一次（必须幂等）。
	runConcurrent(t, unlikers, func(index int) error {
		userID := int64(index + 1)
		result, err := cache.SetActionState(ctx, userID, clipID, dominteraction.ActionLike, false)
		if err != nil {
			return err
		}
		apply(userID, result)
		if index < 20 {
			again, againErr := cache.SetActionState(ctx, userID, clipID, dominteraction.ActionLike, false)
			if againErr != nil {
				return againErr
			}
			if again.Changed {
				t.Errorf("重复取消 user=%d 不应再次改变计数", userID)
			}
			apply(userID, again)
		}
		return nil
	})

	shards, total = likeShardSums(t, cache, clipID)
	expected := int64(likers - unlikers)
	if total != expected {
		t.Fatalf("取消后分片总和 = %d，期望 %d（分片=%v）", total, expected, shards)
	}
	if db := dbActiveCount(); db != int(total) {
		t.Fatalf("分片总和 %d 与 DB 活跃行数 %d 不一致", total, db)
	}
	for shard, value := range shards {
		if value < 0 {
			t.Fatalf("分片 %d 计数为负: %d", shard, value)
		}
	}

	// 阶段三：剩余 60 位读者全部取消，分片总和回到 0，任一分片不得为负。
	runConcurrent(t, likers-unlikers, func(index int) error {
		userID := int64(unlikers + index + 1)
		result, err := cache.SetActionState(ctx, userID, clipID, dominteraction.ActionLike, false)
		if err != nil {
			return err
		}
		apply(userID, result)
		return nil
	})

	shards, total = likeShardSums(t, cache, clipID)
	if total != 0 {
		t.Fatalf("全部取消后分片总和 = %d，期望 0（分片=%v）", total, shards)
	}
	if db := dbActiveCount(); db != 0 {
		t.Fatalf("全部取消后 DB 活跃行数 = %d，期望 0", db)
	}
}

// TestSetActionStateSameUserConcurrentLikesCountOnce 验证同一读者并发点赞时 WATCH 冲突重试
// 只允许一次计数变化：分片总和必须为 1，且 Changed=true 的调用恰好一次。
func TestSetActionStateSameUserConcurrentLikesCountOnce(t *testing.T) {
	ctx := context.Background()
	cache, _ := newActionTestCache(t)
	const clipID int64 = 9002
	const userID int64 = 77
	const goroutines = 20

	var mu sync.Mutex
	changedCount := 0
	runConcurrent(t, goroutines, func(int) error {
		result, err := cache.SetActionState(ctx, userID, clipID, dominteraction.ActionLike, true)
		if err != nil {
			return err
		}
		if result.Changed {
			mu.Lock()
			changedCount++
			mu.Unlock()
		}
		return nil
	})

	shards, total := likeShardSums(t, cache, clipID)
	if total != 1 {
		t.Fatalf("同一读者并发点赞后分片总和 = %d，期望 1（分片=%v）", total, shards)
	}
	if changedCount != 1 {
		t.Fatalf("Changed=true 的调用次数 = %d，期望恰好 1", changedCount)
	}
	if state, err := cache.client.Get(ctx, actionStateKey(clipID, userID, dominteraction.ActionLike)).Result(); err != nil || state != "1" {
		t.Fatalf("状态键 = %q (err=%v)，期望 \"1\"", state, err)
	}
}

// runConcurrent 并发执行 n 个任务并汇总首个错误，避免在 goroutine 内直接调用 t.Fatalf。
func runConcurrent(t *testing.T, n int, fn func(index int) error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := fn(index); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发任务失败: %v", err)
		}
	}
}

// countNonZero 统计非零分片数量，用于断言用户 ID 散列确实分散到多个分片。
func countNonZero(values []int64) int {
	count := 0
	for _, value := range values {
		if value != 0 {
			count++
		}
	}
	return count
}
