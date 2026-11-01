// fanout_worker.go 实现 FanoutWorker：消费 clip.published 事件，
// 预热卡片/计数缓存后按创作者粉丝规模分流——大 V 写 outbox（拉模型），
// 普通创作者按 500/批把新 clip 推入全部粉丝的 inbox（推模型，读路径直接命中）。
package appclip

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
)

// fanoutBatchSize 是单次 inbox 写入的粉丝批大小。
const fanoutBatchSize = 500

// FanoutWorker 是 clip.published 队列的消费者，把新发布内容扇出到关注流缓存。
type FanoutWorker struct {
	repo  domfeed.Repository
	cache appfeed.FollowingCache
}

// NewFanoutWorker 创建发布扇出 Worker；cache 为 nil 或未启用时事件直接确认（读路径回源 DB）。
func NewFanoutWorker(repo domfeed.Repository, cache appfeed.FollowingCache) *FanoutWorker {
	return &FanoutWorker{repo: repo, cache: cache}
}

// Name 返回 Worker 名称，供启动日志区分。
func (w *FanoutWorker) Name() string {
	return "clip_fanout"
}

// Handle 解析事件并执行扇出：非法载荷与已删除 clip 直接确认；
// 缓存写失败返回错误交由 MQ 重新入队（ZADD 幂等，重复投递安全），避免部分扇出造成永久丢失。
func (w *FanoutWorker) Handle(ctx context.Context, body []byte) error {
	var event ClipPublished
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("warning: 丢弃非法 clip.published 载荷: %v", err)
		return nil
	}
	if event.ClipID <= 0 || event.CreatorID <= 0 {
		log.Printf("warning: 丢弃字段非法的 clip.published 载荷 (clip_id=%d creator_id=%d)",
			event.ClipID, event.CreatorID)
		return nil
	}
	if w.cache == nil || !w.cache.Enabled() {
		// 缓存未启用时 following feed 走 DB 回源，扇出无意义。
		return nil
	}

	// 预热卡片与计数缓存：随后粉丝的 following feed 首读直接命中，无需回源。
	entries, err := w.repo.BatchGetEntries(ctx, []int64{event.ClipID})
	if err != nil {
		return fmt.Errorf("回源 clip 条目失败 (clip_id=%d): %w", event.ClipID, err)
	}
	entry, ok := entries[event.ClipID]
	if !ok {
		// clip 已删除：扇出无意义且重试不会成功，确认消息。
		log.Printf("warning: clip.published 对应 clip 不可见，丢弃 (clip_id=%d)", event.ClipID)
		return nil
	}
	stats, err := w.repo.BatchGetStats(ctx, []int64{event.ClipID})
	if err != nil {
		return fmt.Errorf("回源 clip 计数失败 (clip_id=%d): %w", event.ClipID, err)
	}
	w.cache.SetCards(ctx, []*domfeed.FeedEntry{entry})
	w.cache.SetStats(ctx, stats)

	boxEntry := domfeed.InboxEntry{
		ClipID:      entry.ClipID,
		CreatorID:   entry.CreatorID,
		PublishedAt: entry.PublishedAt,
	}

	followerCount, err := w.repo.CountFollowers(ctx, event.CreatorID)
	if err != nil {
		return fmt.Errorf("读取粉丝数失败 (creator_id=%d): %w", event.CreatorID, err)
	}
	if followerCount >= appfeed.BigVFollowerThreshold {
		if err := w.cache.AddOutboxEntries(ctx, event.CreatorID, []domfeed.InboxEntry{boxEntry}); err != nil {
			return fmt.Errorf("写创作者 outbox 失败 (creator_id=%d): %w", event.CreatorID, err)
		}
		log.Printf("clip_fanout path=outbox clip_id=%d creator_id=%d followers=%d",
			event.ClipID, event.CreatorID, followerCount)
		return nil
	}

	followerIDs, err := w.repo.ListFollowerIDs(ctx, event.CreatorID)
	if err != nil {
		return fmt.Errorf("读取粉丝列表失败 (creator_id=%d): %w", event.CreatorID, err)
	}
	batches := 0
	for start := 0; start < len(followerIDs); start += fanoutBatchSize {
		end := start + fanoutBatchSize
		if end > len(followerIDs) {
			end = len(followerIDs)
		}
		for _, followerID := range followerIDs[start:end] {
			if err := w.cache.AddInboxEntries(ctx, followerID, []domfeed.InboxEntry{boxEntry}); err != nil {
				return fmt.Errorf("写粉丝 inbox 失败 (clip_id=%d follower_id=%d): %w", event.ClipID, followerID, err)
			}
		}
		batches++
	}
	log.Printf("clip_fanout path=inbox clip_id=%d creator_id=%d followers=%d batches=%d",
		event.ClipID, event.CreatorID, len(followerIDs), batches)
	return nil
}
