// backfill.go 实现关注成功后的 Feed 回填：把创作者最近发布写入读者 inbox（推模型）
// 或创作者 outbox（大 V 拉模型），供 relation 服务在关注状态迁移时同步触发。
package appfeed

import (
	"context"

	domfeed "pulsefeed/internal/domain/feed"
)

// backfillRecentLimit 是关注回填的创作者最近发布条数上限。
const backfillRecentLimit = 100

// InboxBackfiller 是 relation 域 FollowFeedBackfiller 端口的 feed 侧实现，由组合根注入。
type InboxBackfiller struct {
	repo  domfeed.Repository
	cache FollowingCache
}

// NewInboxBackfiller 创建回填器；cache 为 nil 或未启用时回填静默跳过（读路径回源 DB）。
func NewInboxBackfiller(repo domfeed.Repository, cache FollowingCache) *InboxBackfiller {
	return &InboxBackfiller{repo: repo, cache: cache}
}

// BackfillFollowing 在 followerID 关注 creatorID 成功后回填其最近发布：
// 大 V（粉丝数达阈值）写入创作者 outbox（读时合并），普通创作者写入读者 inbox（推模型）。
func (b *InboxBackfiller) BackfillFollowing(ctx context.Context, followerID, creatorID int64) error {
	if b.cache == nil || !b.cache.Enabled() {
		return nil
	}
	followerCount, err := b.repo.CountFollowers(ctx, creatorID)
	if err != nil {
		return err
	}
	clips, err := b.repo.ListCreatorRecentClips(ctx, creatorID, backfillRecentLimit)
	if err != nil {
		return err
	}
	if len(clips) == 0 {
		return nil
	}
	entries := make([]domfeed.InboxEntry, 0, len(clips))
	for _, clip := range clips {
		entries = append(entries, domfeed.InboxEntry{
			ClipID:      clip.ClipID,
			CreatorID:   clip.CreatorID,
			PublishedAt: clip.PublishedAt,
		})
	}
	if followerCount >= BigVFollowerThreshold {
		return b.cache.AddOutboxEntries(ctx, creatorID, entries)
	}
	return b.cache.AddInboxEntries(ctx, followerID, entries)
}
