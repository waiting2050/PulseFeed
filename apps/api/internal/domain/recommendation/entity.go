// entity.go 定义推荐领域的候选、排序结果、权重常量与排序/交错算法。
package domrecommendation

import (
	"math"
	"sort"
	"time"

	domembedding "pulsefeed/internal/domain/embedding"
	domfeed "pulsefeed/internal/domain/feed"
)

const (
	// WeightHot 是候选热度分的权重，主导推荐排序。
	WeightHot = 0.70
	// WeightInterest 是候选与读者兴趣向量余弦相似度的权重。
	WeightInterest = 0.20
	// WeightFreshness 是发布时间新鲜度的权重。
	WeightFreshness = 0.10

	// ImpressionWindowDays 是候选池排除最近曝光的天数窗口。
	ImpressionWindowDays = 7
	// InterestWindowDays 是兴趣向量统计最近播放行为的天数窗口。
	InterestWindowDays = 30
	// InterestVectorLimit 是参与兴趣向量聚合的曝光行上限。
	InterestVectorLimit = 200

	// CandidatePoolMin 是候选池最小召回条数。
	CandidatePoolMin = 50
	// CandidatePoolMax 是候选池最大召回条数。
	CandidatePoolMax = 500
)

// Candidate 是候选池中的一条 clip：排序所需的计数、发布时间与文本向量。
type Candidate struct {
	ClipID       int64
	CreatorID    int64
	PublishedAt  time.Time
	LikeCount    int64
	SaveCount    int64
	CommentCount int64
	PlayCount    int64
	Embedding    domembedding.Vector
}

// RankedClip 是排序后的候选：Score 由权重公式计算，作为游标过滤键的一部分。
type RankedClip struct {
	ClipID      int64
	CreatorID   int64
	PublishedAt time.Time
	Score       float64
}

// InterestClip 是兴趣向量聚合使用的一条曝光记录，AddWeight 为 play/complete 加权值。
type InterestClip struct {
	ClipID         int64
	PlayCount      int64
	CompletedCount int64
}

// ImpressionRecord 是内部曝光查询使用的一条按日聚合记录。
type ImpressionRecord struct {
	ViewerID       int64
	ClipID         int64
	Day            string
	PlayCount      int64
	CompletedCount int64
	FirstPlayedAt  time.Time
	LastPlayedAt   time.Time
}

// ImpressionDecision 是内部接口给出的候选可展示决策。
type ImpressionDecision struct {
	ShouldShow   bool
	Reason       string
	LastPlayedAt *time.Time
}

// Rank 按 0.70 热度 + 0.20 兴趣 + 0.10 新鲜度计算候选分数并降序排序；
// 热度以候选池内最大值为基准归一化到 [0,1]，兴趣余弦负值截断为 0，
// 分数相同按 clip_id 降序兜底，保证排序与游标边界完全确定。
func Rank(candidates []Candidate, interest domembedding.Vector, snapshotAt time.Time) []RankedClip {
	if len(candidates) == 0 {
		return nil
	}

	hots := make([]float64, len(candidates))
	maxHot := 0.0
	for i, candidate := range candidates {
		hots[i] = ClipHotScore(candidate, snapshotAt)
		if hots[i] > maxHot {
			maxHot = hots[i]
		}
	}

	ranked := make([]RankedClip, 0, len(candidates))
	for i, candidate := range candidates {
		hotComponent := 0.0
		if maxHot > 0 {
			hotComponent = hots[i] / maxHot
		}
		interestComponent := 0.0
		if len(candidate.Embedding) > 0 && len(interest) > 0 {
			interestComponent = math.Max(0, candidate.Embedding.Cosine(interest))
		}
		ranked = append(ranked, RankedClip{
			ClipID:      candidate.ClipID,
			CreatorID:   candidate.CreatorID,
			PublishedAt: candidate.PublishedAt,
			Score:       WeightHot*hotComponent + WeightInterest*interestComponent + WeightFreshness*freshness(candidate.PublishedAt, snapshotAt),
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].ClipID > ranked[j].ClipID
		}
		return ranked[i].Score > ranked[j].Score
	})
	return ranked
}

// ClipHotScore 复用 feed 领域的热度公式，以 snapshotAt 为时间基准计算候选热度分，
// 保证跨分钟翻页时同一候选的分数不随时间漂移。
func ClipHotScore(candidate Candidate, snapshotAt time.Time) float64 {
	return domfeed.ScoreHot(domfeed.EntryStats{
		LikeCount:    candidate.LikeCount,
		SaveCount:    candidate.SaveCount,
		CommentCount: candidate.CommentCount,
		PlayCount:    candidate.PlayCount,
	}, candidate.PublishedAt, snapshotAt)
}

// InterestWeight 计算一条曝光记录对兴趣向量的权重：播放计 1、完播额外计 2。
func InterestWeight(playCount, completedCount int64) float64 {
	return float64(playCount) + 2*float64(completedCount)
}

// InterleaveByCreator 按创作者做轮转交错：同一创作者内部保持排序先后，
// 不同创作者按首次出现顺序轮流取一条，避免单创作者连续霸屏；只改变展示顺序。
func InterleaveByCreator(items []RankedClip) []RankedClip {
	if len(items) <= 1 {
		return items
	}

	order := make([]int64, 0, len(items))
	groups := make(map[int64][]RankedClip, len(items))
	for _, item := range items {
		if _, ok := groups[item.CreatorID]; !ok {
			order = append(order, item.CreatorID)
		}
		groups[item.CreatorID] = append(groups[item.CreatorID], item)
	}

	result := make([]RankedClip, 0, len(items))
	for len(result) < len(items) {
		for _, creatorID := range order {
			group := groups[creatorID]
			if len(group) == 0 {
				continue
			}
			result = append(result, group[0])
			groups[creatorID] = group[1:]
		}
	}
	return result
}

// freshness 计算发布时间新鲜度：以小时年龄做 24 小时半衰式递减，范围 (0,1]。
func freshness(publishedAt, snapshotAt time.Time) float64 {
	ageHours := snapshotAt.Sub(publishedAt).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	return 1 / (1 + ageHours/24)
}
