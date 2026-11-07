// repository.go 定义 embedding 领域的仓储端口，应用层只依赖该接口。
package domembedding

import "context"

// Repository 描述向量生成与读取的持久化能力，由 infra 层实现。
type Repository interface {
	// GetClipTitle 读取已发布 clip 的标题文本，供 hash n-gram 生成向量；
	// clip 不存在或非发布态返回 ErrClipNotFound。
	GetClipTitle(ctx context.Context, clipID int64) (string, error)
	// Upsert 写入或覆盖 clip 向量（同一 clip 只保留一份最新向量）。
	Upsert(ctx context.Context, clipID int64, vector Vector) error
	// BatchGet 批量读取向量，缺失向量的 clip 缺席于结果集。
	BatchGet(ctx context.Context, clipIDs []int64) (map[int64]Vector, error)
}
