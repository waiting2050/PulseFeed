// repository.go 定义 playback 领域的仓储端口，应用层只依赖该接口。
package domplayback

import "context"

// Repository 描述播放配置与 QoS 上报的持久化能力。
type Repository interface {
	// GetOrCreateConfig 按用户 ID 读取播放配置；未命中时返回默认配置（不落库）。
	GetOrCreateConfig(ctx context.Context, userID int64) (*Config, error)
	// UpsertConfig 写入或更新用户播放配置；返回最终落库值。
	UpsertConfig(ctx context.Context, cfg *Config) (*Config, error)
	// CreateQoS 插入一条 QoS 上报；唯一索引 uk_event_id 已存在时静默忽略（幂等）。
	// inserted=true 表示本次真正写入了新行，并在同一事务内按「播放开始」口径
	// 累加 pf_clip_stats.play_count（同 event_id 已在播放事件表出现时跳过）。
	CreateQoS(ctx context.Context, report *QoSReport) (inserted bool, err error)
}

// ClipReader 描述应用层从 feed 域取预加载媒体列表的端口；
// 放在 playback 域接口中避免 playback 反向依赖 feed 仓储。
type ClipReader interface {
	// ListLatestMedia 按 (published_at, id) 递减返回上限 limit 条媒体信息，供预加载。
	ListLatestMedia(ctx context.Context, limit int) ([]PreloadMedia, error)
}
