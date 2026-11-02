// worker.go 实现 ClipEmbeddingWorker：消费 clip.embedding 事件并为对应 clip 生成文本向量。
package appembedding

import (
	"context"
	"encoding/json"
	"log"
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

// Handle 解析事件并生成向量：非法载荷直接确认，领域终态（clip 不可见）由服务层跳过，
// 其余错误返回给 MQ 重新入队。
func (w *Worker) Handle(ctx context.Context, body []byte) error {
	var event ClipEmbeddingRequest
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("warning: 丢弃非法 clip.embedding 载荷: %v", err)
		return nil
	}
	if event.ClipID <= 0 {
		log.Printf("warning: 丢弃字段非法的 clip.embedding 载荷 (clip_id=%d)", event.ClipID)
		return nil
	}
	return w.service.Generate(ctx, event.ClipID)
}
