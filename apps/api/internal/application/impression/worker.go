// worker.go 保留播放事件队列的消费者外壳：synced 补偿链路已删除，
// 曝光聚合已在 API 同步落库事务内完成，消费端不再做任何业务处理。
package appimpression

import "context"

// PlayEventWorker 消费 impression.play_recorded 队列。
// 补偿逻辑删除后仅用于排空队列，避免消息积压；队列与绑定暂不改动，待下线方案确认后统一处理。
type PlayEventWorker struct{}

// NewPlayEventWorker 创建播放事件消费者（当前为排空型消费者）。
func NewPlayEventWorker() *PlayEventWorker {
	return &PlayEventWorker{}
}

// Name 返回 Worker 名称，供启动日志与指标区分。
func (w *PlayEventWorker) Name() string {
	return "play_event"
}

// Handle 直接确认消息；曝光聚合已由 API 同步事务完成，消费端不再补偿或改写事件行。
func (w *PlayEventWorker) Handle(_ context.Context, _ []byte) error {
	return nil
}
