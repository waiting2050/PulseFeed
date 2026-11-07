// repository.go 定义曝光领域的仓储端口，应用层只依赖该接口。
package domimpression

import "context"

// Repository 描述播放事件落库与曝光聚合的持久化能力，由 infra 层实现。
type Repository interface {
	// RecordPlayEvent 幂等写入一条展示/播放事件：同 EventID 已存在时为 no-op；
	// 仅首次写入时按事件类型更新 (viewer_id, clip_id, day) 聚合行：
	// exposed/skip 只登记展示时间，play 累加 play_count，complete 累加 completed_count；
	// 只有 play 事件按「播放开始」口径累加 pf_clip_stats.play_count
	//（同 event_id 已在 QoS 表出现时跳过），返回是否为新事件。
	// clip 不存在或非发布态返回 ErrClipNotFound。
	RecordPlayEvent(ctx context.Context, event *PlayEvent) (isNew bool, err error)
	// UpsertImpression 按事件类型把一条事件的贡献累加进 (viewer_id, clip_id, day) 聚合行，
	// 用于 Worker 补偿链路：API 进程崩溃导致聚合未完成时，由 Worker 二次调用保证最终一致。
	UpsertImpression(ctx context.Context, event *PlayEvent) error
	// MarkEventSynced 把播放事件标记为已完成曝光聚合，供 Worker 判定是否需要补偿。
	MarkEventSynced(ctx context.Context, eventID string) error
	// IsEventSynced 查询播放事件是否已完成曝光聚合。
	IsEventSynced(ctx context.Context, eventID string) (bool, error)
}
