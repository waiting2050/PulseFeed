// repository.go 定义 recommendation 领域的仓储端口，应用层只依赖该接口。
package domrecommendation

import (
	"context"
	"time"
)

// Repository 描述候选召回、兴趣行为与曝光查询的持久化能力，由 infra 层实现。
type Repository interface {
	// ListCandidates 按 snapshotAt 固定候选池：只取该时刻前已发布的 clip，
	// 排除读者最近 ImpressionWindowDays 天内曝光过的 clip，
	// 按 snapshotAt 基准的热度降序（hot_score DESC, published_at DESC, id DESC）召回至多 limit 条。
	ListCandidates(ctx context.Context, viewerID int64, snapshotAt time.Time, limit int) ([]Candidate, error)
	// ListInterestClips 返回读者在 [since, until] 内曝光过的 clip 及其播放/完播计数，用于兴趣向量聚合。
	ListInterestClips(ctx context.Context, viewerID int64, since, until time.Time, limit int) ([]InterestClip, error)
	// GetImpression 读取读者对指定 clip 最近的一条曝光聚合记录；不存在返回 ErrImpressionNotFound。
	GetImpression(ctx context.Context, viewerID, clipID int64) (*ImpressionRecord, error)
	// ListImpressions 返回读者最近 since 之后的曝光聚合记录，按 last_played_at 降序。
	ListImpressions(ctx context.Context, viewerID int64, since time.Time, limit int) ([]*ImpressionRecord, error)
}
