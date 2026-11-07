// worker.go 实现 ClipEmbeddingWorker：消费 clip.embedding 事件并为对应 clip 生成文本向量。
package appembedding

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"pulsefeed/internal/domain/messaging"
)

// ClipEmbeddingRequest 是 clip.embedding 事件载荷，与 clip.published 共享 clip_id 字段。
type ClipEmbeddingRequest struct {
	ClipID    int64  `json:"clip_id"`
	CreatorID int64  `json:"creator_id"`
	Title     string `json:"title"`
}

// Worker 是 clip.embedding 队列的消费者。
type Worker struct {
	service *Service
}

// NewWorker 创建向量生成 Worker。
func NewWorker(service *Service) *Worker {
	return &Worker{service: service}
}

// Name 返回 Worker 名称，供启动日志区分。
func (w *Worker) Name() string {
	return "clip_embedding"
}

// Handle 解析事件并生成向量：非法载荷与领域终态（clip 不可见）标记为不可重试（转入死信），
// 其余错误返回给 MQ 重新入队。
func (w *Worker) Handle(ctx context.Context, body []byte) error {
	var event ClipEmbeddingRequest
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("warning: clip.embedding 载荷非法，转入死信队列: %v", err)
		return messaging.Permanent(fmt.Errorf("解析 clip.embedding 载荷失败: %w", err))
	}
	if event.ClipID <= 0 {
		log.Printf("warning: clip.embedding 字段非法，转入死信队列 (clip_id=%d)", event.ClipID)
		return messaging.Permanent(fmt.Errorf("clip.embedding 字段非法 (clip_id=%d)", event.ClipID))
	}
	return w.service.Generate(ctx, event.ClipID)
}
