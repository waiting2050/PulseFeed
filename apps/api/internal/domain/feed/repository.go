// repository.go 定义 feed 领域的仓储端口，应用层只依赖该接口。
package domfeed

import "context"

// Repository 描述 feed 的读取能力，由 infra 层实现。
type Repository interface {
	// ListLatestPage 按 (published_at, id) 严格递减返回可见 clip 条目（含计数），
	// cursor 为 nil 表示首页；limit 由调用方按页大小 +1 传入以判定是否还有下一页。
	ListLatestPage(ctx context.Context, cursor *Cursor, limit int) ([]*FeedEntry, error)
	// BatchGetEntries 按 ID 批量读取条目，供卡片缓存回源使用（S8/S9 链路补齐）。
	BatchGetEntries(ctx context.Context, clipIDs []int64) (map[int64]*FeedEntry, error)
	// BatchGetStats 按 ID 批量读取互动计数，供计数缓存回源使用（S9 补齐）。
	BatchGetStats(ctx context.Context, clipIDs []int64) (map[int64]*EntryStats, error)
	// BatchGetViewerStates 批量读取读者对条目的互动状态（S9 互动链路补齐）。
	BatchGetViewerStates(ctx context.Context, viewerID int64, clipIDs []int64) (map[int64]*ViewerState, error)
}
