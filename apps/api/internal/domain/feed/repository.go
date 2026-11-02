// repository.go 定义 feed 领域的仓储端口，应用层只依赖该接口。
package domfeed

import "context"

// Repository 描述 feed 的读取能力，由 infra 层实现。
type Repository interface {
	// ListLatestPage 按 (published_at, id) 严格递减返回可见 clip 条目（含计数），
	// cursor 为 nil 表示首页；limit 由调用方按页大小 +1 传入以判定是否还有下一页。
	ListLatestPage(ctx context.Context, cursor *Cursor, limit int) ([]*FeedEntry, error)
	// ListFollowingPage 按同一复合游标返回读者关注创作者的可见 clip 条目（含计数），
	// 是 following 场景在缓存不可用时的 DB 回源路径（S11 接入）。
	ListFollowingPage(ctx context.Context, viewerID int64, cursor *Cursor, limit int) ([]*FeedEntry, error)
	// ListHotPage 按计算出的热度分严格递减返回可见 clip 条目（含计数与分数），
	// 是 hot 场景 Redis 分钟桶不可用/为空时的 DB 回源路径（S15 接入）。
	ListHotPage(ctx context.Context, cursor *Cursor, limit int) ([]*FeedEntry, error)
	// ListFollowedCreators 返回读者关注中的创作者及其粉丝数，供大 V outbox 合并判定（S11 接入）。
	ListFollowedCreators(ctx context.Context, viewerID int64) ([]*FollowedCreator, error)
	// CountFollowers 返回创作者的有效粉丝数（pf_user_stats 维护值），供大 V 判定与扇出阈值使用。
	CountFollowers(ctx context.Context, creatorID int64) (int64, error)
	// ListFollowerIDs 返回创作者的全部有效粉丝 ID，供发布扇出按批写粉丝 inbox（S12 接入）。
	ListFollowerIDs(ctx context.Context, creatorID int64) ([]int64, error)
	// ListCreatorRecentClips 返回创作者最近发布的 N 条可见 clip，供关注回填取数（S11 接入）。
	ListCreatorRecentClips(ctx context.Context, creatorID int64, limit int) ([]*FeedEntry, error)
	// ListLatestClipIDs 按 (published_at, id) 严格递减返回最近发布的 N 个可见 clip_id，
	// 供 playback 预加载列表使用（S16 接入）。
	ListLatestClipIDs(ctx context.Context, limit int) ([]int64, error)
	// BatchGetEntries 按 ID 批量读取条目（不含计数），供卡片缓存回源使用（S11 起消费）。
	BatchGetEntries(ctx context.Context, clipIDs []int64) (map[int64]*FeedEntry, error)
	// BatchGetStats 按 ID 批量读取互动计数，供计数缓存回源使用（S11 起消费）。
	BatchGetStats(ctx context.Context, clipIDs []int64) (map[int64]*EntryStats, error)
	// BatchGetViewerStates 批量读取读者对条目的互动状态（S9 互动链路补齐）。
	BatchGetViewerStates(ctx context.Context, viewerID int64, clipIDs []int64) (map[int64]*ViewerState, error)
}
