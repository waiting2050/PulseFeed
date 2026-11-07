// clip_embedding_worker_test.go 用内存 fake 验证 ClipEmbeddingWorker 的载荷校验、256 维向量生成、
// clip 不可见时的终态确认、仓储错误触发的 MQ 重投以及同一 clip 重复处理的幂等契约。
package appembedding_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	appembedding "pulsefeed/internal/application/embedding"
	domembedding "pulsefeed/internal/domain/embedding"
	"pulsefeed/internal/domain/messaging"
)

// embeddingFakeRepository 是 embedding 仓储的内存替身，记录调用次数并只保留每个 clip 的最后一版向量。
type embeddingFakeRepository struct {
	titles      map[int64]string
	titleErrors map[int64]error
	vectors     map[int64]domembedding.Vector
	titleCalls  int
	upsertCalls int
	upsertErr   error
}

var _ domembedding.Repository = (*embeddingFakeRepository)(nil)

// newEmbeddingFakeRepository 创建空的内存仓储。
func newEmbeddingFakeRepository() *embeddingFakeRepository {
	return &embeddingFakeRepository{
		titles:      make(map[int64]string),
		titleErrors: make(map[int64]error),
		vectors:     make(map[int64]domembedding.Vector),
	}
}

// GetClipTitle 返回预置标题；未登记或预置错误的 clip 返回对应结果。
func (f *embeddingFakeRepository) GetClipTitle(_ context.Context, clipID int64) (string, error) {
	f.titleCalls++
	if err, ok := f.titleErrors[clipID]; ok {
		return "", err
	}
	title, ok := f.titles[clipID]
	if !ok {
		return "", domembedding.ErrClipNotFound
	}
	return title, nil
}

// Upsert 按 clip_id 覆盖写入向量，模拟唯一键 upsert 语义。
func (f *embeddingFakeRepository) Upsert(_ context.Context, clipID int64, vector domembedding.Vector) error {
	f.upsertCalls++
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.vectors[clipID] = append(domembedding.Vector(nil), vector...)
	return nil
}

// BatchGet 返回已写入的向量，缺失的 clip 缺席于结果集。
func (f *embeddingFakeRepository) BatchGet(_ context.Context, clipIDs []int64) (map[int64]domembedding.Vector, error) {
	found := make(map[int64]domembedding.Vector, len(clipIDs))
	for _, clipID := range clipIDs {
		if vector, ok := f.vectors[clipID]; ok {
			found[clipID] = vector
		}
	}
	return found, nil
}

// TestClipEmbeddingWorkerDeadLettersInvalidPayload 验证非法 JSON 与非法 clip_id 标记为不可重试（转入死信）且不触碰仓储。
func TestClipEmbeddingWorkerDeadLettersInvalidPayload(t *testing.T) {
	repo := newEmbeddingFakeRepository()
	worker := appembedding.NewWorker(appembedding.New(repo))

	payloads := [][]byte{
		[]byte("not-json"),
		[]byte(`{}`),
		[]byte(`{"clip_id":0,"creator_id":7}`),
		[]byte(`{"clip_id":-3,"creator_id":7}`),
	}
	for _, body := range payloads {
		if err := worker.Handle(context.Background(), body); !messaging.IsPermanent(err) {
			t.Fatalf("非法载荷 %s 应标记不可重试（转入死信）, 实际返回 %v", body, err)
		}
	}
	if repo.titleCalls != 0 || repo.upsertCalls != 0 {
		t.Fatalf("非法载荷不应访问仓储, title_calls=%d upsert_calls=%d", repo.titleCalls, repo.upsertCalls)
	}
}

// TestClipEmbeddingWorkerGeneratesDimension256Vector 验证合法 clip 生成归一化的 256 维向量并按 clip_id 写入。
func TestClipEmbeddingWorkerGeneratesDimension256Vector(t *testing.T) {
	const clipID int64 = 42
	const title = "脉冲星球 剪辑 03"

	repo := newEmbeddingFakeRepository()
	repo.titles[clipID] = title

	body, err := json.Marshal(appembedding.ClipEmbeddingRequest{
		ClipID:    clipID,
		CreatorID: 7,
		Title:     "载荷中的标题不应作为向量来源",
	})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	worker := appembedding.NewWorker(appembedding.New(repo))
	if worker.Name() != "clip_embedding" {
		t.Fatalf("Worker 名称 = %q, 期望 clip_embedding", worker.Name())
	}
	if err := worker.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle 返回错误: %v", err)
	}
	if repo.titleCalls != 1 || repo.upsertCalls != 1 {
		t.Fatalf("仓储调用次数 title=%d upsert=%d, 期望各 1", repo.titleCalls, repo.upsertCalls)
	}

	vector, ok := repo.vectors[clipID]
	if !ok {
		t.Fatalf("clip %d 未写入向量", clipID)
	}
	if len(vector) != domembedding.Dimension {
		t.Fatalf("向量维度 = %d, 期望 %d", len(vector), domembedding.Dimension)
	}
	if vector.IsZero() {
		t.Fatalf("非空标题不应生成零向量")
	}

	// 向量必须由仓储标题生成，而不是事件载荷中的 Title 字段。
	if similarity := vector.Cosine(domembedding.HashText(title)); similarity < 0.999999 {
		t.Fatalf("向量与仓储标题的 hash 向量余弦相似度 = %.9f, 期望 1", similarity)
	}

	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-9 {
		t.Fatalf("向量 L2 范数 = %.12f, 期望 1", math.Sqrt(norm))
	}
}

// TestClipEmbeddingWorkerDeadLettersInvisibleClip 验证 clip 不存在/非发布态时标记不可重试（转入死信）。
func TestClipEmbeddingWorkerDeadLettersInvisibleClip(t *testing.T) {
	repo := newEmbeddingFakeRepository()
	body, err := json.Marshal(appembedding.ClipEmbeddingRequest{ClipID: 99, CreatorID: 7})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	worker := appembedding.NewWorker(appembedding.New(repo))
	if err := worker.Handle(context.Background(), body); !messaging.IsPermanent(err) {
		t.Fatalf("clip 不可见应标记不可重试（转入死信）, 实际返回 %v", err)
	}
	if repo.upsertCalls != 0 {
		t.Fatalf("clip 不可见不应写入向量, upsert_calls=%d", repo.upsertCalls)
	}
}

// TestClipEmbeddingWorkerReturnsErrorForMQRetry 验证持久化错误向上返回，由 MQ 重新投递。
func TestClipEmbeddingWorkerReturnsErrorForMQRetry(t *testing.T) {
	const clipID int64 = 42
	body, err := json.Marshal(appembedding.ClipEmbeddingRequest{ClipID: clipID, CreatorID: 7})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	t.Run("读取标题失败", func(t *testing.T) {
		repo := newEmbeddingFakeRepository()
		repo.titleErrors[clipID] = errors.New("读取标题失败")
		worker := appembedding.NewWorker(appembedding.New(repo))

		if err := worker.Handle(context.Background(), body); err == nil {
			t.Fatalf("读取标题失败应返回错误触发重投")
		}
		if repo.upsertCalls != 0 {
			t.Fatalf("读取标题失败不应写入向量, upsert_calls=%d", repo.upsertCalls)
		}
	})

	t.Run("写入向量失败", func(t *testing.T) {
		repo := newEmbeddingFakeRepository()
		repo.titles[clipID] = "任意标题"
		repo.upsertErr = errors.New("写入向量失败")
		worker := appembedding.NewWorker(appembedding.New(repo))

		if err := worker.Handle(context.Background(), body); err == nil {
			t.Fatalf("写入向量失败应返回错误触发重投")
		}
	})
}

// TestClipEmbeddingWorkerRepeatHandlingKeepsSingleVector 验证同一 clip 重复消费走 upsert，向量表只保留一行。
func TestClipEmbeddingWorkerRepeatHandlingKeepsSingleVector(t *testing.T) {
	const clipID int64 = 42
	const title = "同一 clip 重复生成"

	repo := newEmbeddingFakeRepository()
	repo.titles[clipID] = title
	body, err := json.Marshal(appembedding.ClipEmbeddingRequest{ClipID: clipID, CreatorID: 7})
	if err != nil {
		t.Fatalf("序列化事件失败: %v", err)
	}

	worker := appembedding.NewWorker(appembedding.New(repo))
	for i := 0; i < 2; i++ {
		if err := worker.Handle(context.Background(), body); err != nil {
			t.Fatalf("第 %d 次处理返回错误: %v", i+1, err)
		}
	}

	if len(repo.vectors) != 1 {
		t.Fatalf("同一 clip 重复处理后向量行数 = %d, 期望 1", len(repo.vectors))
	}
	if repo.upsertCalls != 2 {
		t.Fatalf("upsert_calls = %d, 期望 2（重复消息仍走 upsert）", repo.upsertCalls)
	}
	if similarity := repo.vectors[clipID].Cosine(domembedding.HashText(title)); similarity < 0.999999 {
		t.Fatalf("重复处理后向量余弦相似度 = %.9f, 期望 1（结果应保持一致）", similarity)
	}
}
