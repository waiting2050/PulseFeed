// repository.go 定义曝光领域的仓储端口，应用层只依赖该接口。
package domimpression

import "context"

// OutboxEvent 是与播放事件同事务落库、由 relay 异步投递的事件记录。
// 重复投递时下游按 EventID 识别同一事件（配合各自的幂等写），因此 relay 可以安全重试。
type OutboxEvent struct {
	// EventID 是全局唯一的事件 ID，写入 AMQP MessageId 与事件载荷。
	EventID string
	// Topic 是逻辑事件类型，relay 据此解析交换机与路由键。
	Topic string
	// Payload 是已序列化的 JSON 事件载荷。
	Payload []byte
}

// EventBuilder 在落库事务内构造 outbox 事件；isNew 为 false 表示同 event_id 已存在，
// 此时必须返回空事件，避免重复上报产生重复投递。
type EventBuilder func(isNew bool) ([]OutboxEvent, error)

// Repository 描述播放事件落库与曝光聚合的持久化能力，由 infra 层实现。
type Repository interface {
	// RecordPlayEvent 幂等写入一条展示/播放事件：同 EventID 已存在时为 no-op；
	// 仅首次写入时按事件类型更新 (viewer_id, clip_id, day) 聚合行：
	// exposed/skip 只登记展示时间，play 累加 play_count，complete 累加 completed_count；
	// 只有 play 事件按「播放开始」口径累加 pf_clip_stats.play_count
	//（同 event_id 已在 QoS 表出现时跳过），返回是否为新事件。
	// clip 不存在或非发布态返回 ErrClipNotFound。
	RecordPlayEvent(ctx context.Context, event *PlayEvent) (isNew bool, err error)
	// RecordPlayEventWithEvents 在同一事务内完成 RecordPlayEvent 的全部写入，
	// 并在事件首次写入时调用 build 把 outbox 事件一并落库；
	// 任一写入失败整体回滚，避免“事件落库但消息丢失”或“消息存在但事件回滚”。
	// 同 event_id 重复上报时为 no-op，不产生新的 outbox 事件。
	RecordPlayEventWithEvents(ctx context.Context, event *PlayEvent, build EventBuilder) (isNew bool, err error)
	// UpsertImpression 按事件类型把一条事件的贡献累加进 (viewer_id, clip_id, day) 聚合行，
	// 用于 Worker 补偿链路：API 进程崩溃导致聚合未完成时，由 Worker 二次调用保证最终一致。
	UpsertImpression(ctx context.Context, event *PlayEvent) error
	// MarkEventSynced 把播放事件标记为已完成曝光聚合，供 Worker 判定是否需要补偿。
	MarkEventSynced(ctx context.Context, eventID string) error
	// IsEventSynced 查询播放事件是否已完成曝光聚合。
	IsEventSynced(ctx context.Context, eventID string) (bool, error)
}
