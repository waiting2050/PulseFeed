// service.go 编排 clip 文本向量生成用例：读取标题 → hash n-gram 向量化 → 写入向量表。
package appembedding

import (
	"context"
	"errors"
	"log"

	domembedding "pulsefeed/internal/domain/embedding"
)

// Service 提供向量生成用例，只依赖 embedding 领域端口。
type Service struct {
	repo domembedding.Repository
}

// New 创建向量生成应用服务。
func New(repo domembedding.Repository) *Service {
	return &Service{repo: repo}
}

// Generate 为指定 clip 生成并写入文本向量：clip 已删除或非发布态时视为终态直接跳过，
// 保证消息可确认而不无限重试；其余错误返回给 Worker 重新入队。
func (s *Service) Generate(ctx context.Context, clipID int64) error {
	title, err := s.repo.GetClipTitle(ctx, clipID)
	if err != nil {
		if errors.Is(err, domembedding.ErrClipNotFound) {
			log.Printf("warning: clip 不可见，跳过向量生成 (clip_id=%d)", clipID)
			return nil
		}
		return err
	}

	vector := domembedding.HashText(title)
	if err := s.repo.Upsert(ctx, clipID, vector); err != nil {
		return err
	}
	log.Printf("clip_embedding 生成完成 (clip_id=%d dim=%d)", clipID, len(vector))
	return nil
}
