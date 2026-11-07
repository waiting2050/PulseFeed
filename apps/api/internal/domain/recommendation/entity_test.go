// entity_test.go 校验推荐排序的权重公式、分数兜底与创作者交错顺序。
package domrecommendation

import (
	"math"
	"testing"
	"time"

	domembedding "pulsefeed/internal/domain/embedding"
)

// TestRankOrderByWeightedScore 校验 3 条不同热度候选按手算分数降序排列。
func TestRankOrderByWeightedScore(t *testing.T) {
	snapshot := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ClipID: 3, CreatorID: 1, PublishedAt: snapshot.Add(-time.Hour), LikeCount: 3},
		{ClipID: 2, CreatorID: 1, PublishedAt: snapshot.Add(-30 * time.Minute), LikeCount: 2},
		{ClipID: 1, CreatorID: 1, PublishedAt: snapshot.Add(-2 * time.Hour)},
	}
	ranked := Rank(candidates, nil, snapshot)
	if len(ranked) != 3 {
		t.Fatalf("排序结果条数 = %d, 期望 3", len(ranked))
	}

	wantOrder := []int64{3, 2, 1}
	for i, want := range wantOrder {
		if ranked[i].ClipID != want {
			t.Fatalf("第 %d 条 clip_id = %d, 期望 %d（排序=%v）", i, ranked[i].ClipID, want, ranked)
		}
	}

	// 手算基准：A 热度 9/(1+2)^1.5 归一化为 1，新鲜度 1/(1+1/24)=0.96，分数 0.70+0.10*0.96=0.796。
	if math.Abs(ranked[0].Score-0.796) > 1e-9 {
		t.Fatalf("首条分数 = %.12f, 期望 0.796", ranked[0].Score)
	}
}

// TestRankTieBreakByClipID 校验分数完全相同时按 clip_id 降序，保证排序确定。
func TestRankTieBreakByClipID(t *testing.T) {
	snapshot := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ClipID: 1, CreatorID: 1, PublishedAt: snapshot.Add(-time.Hour), LikeCount: 1},
		{ClipID: 2, CreatorID: 1, PublishedAt: snapshot.Add(-time.Hour), LikeCount: 1},
	}
	ranked := Rank(candidates, nil, snapshot)
	if ranked[0].ClipID != 2 || ranked[1].ClipID != 1 {
		t.Fatalf("同分排序 = [%d,%d], 期望 [2,1]", ranked[0].ClipID, ranked[1].ClipID)
	}
}

// TestRankInterestComponent 校验兴趣相似度参与打分并能压过同分候选的 clip_id 兜底。
func TestRankInterestComponent(t *testing.T) {
	snapshot := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	aligned := make(domembedding.Vector, domembedding.Dimension)
	aligned[0] = 1
	orthogonal := make(domembedding.Vector, domembedding.Dimension)
	orthogonal[1] = 1
	interest := make(domembedding.Vector, domembedding.Dimension)
	interest[0] = 1

	candidates := []Candidate{
		// clip_id 更大，无兴趣时按兜底会排前；兴趣命中后应让位给对齐候选。
		{ClipID: 2, CreatorID: 1, PublishedAt: snapshot.Add(-time.Hour), LikeCount: 1, Embedding: orthogonal},
		{ClipID: 1, CreatorID: 1, PublishedAt: snapshot.Add(-time.Hour), LikeCount: 1, Embedding: aligned},
	}
	ranked := Rank(candidates, interest, snapshot)
	if ranked[0].ClipID != 1 {
		t.Fatalf("兴趣对齐候选未排首 = %v", ranked)
	}
	if diff := ranked[0].Score - ranked[1].Score; math.Abs(diff-WeightInterest) > 1e-9 {
		t.Fatalf("兴趣加权分差 = %.12f, 期望 %.2f", diff, WeightInterest)
	}
}

// TestInterleaveByCreator 校验创作者轮转交错且组内顺序不变。
func TestInterleaveByCreator(t *testing.T) {
	items := []RankedClip{
		{ClipID: 10, CreatorID: 1},
		{ClipID: 8, CreatorID: 1},
		{ClipID: 9, CreatorID: 2},
		{ClipID: 7, CreatorID: 2},
		{ClipID: 6, CreatorID: 3},
	}
	got := InterleaveByCreator(items)
	want := []int64{10, 9, 6, 8, 7}
	if len(got) != len(want) {
		t.Fatalf("交错结果条数 = %d, 期望 %d", len(got), len(want))
	}
	for i, clipID := range want {
		if got[i].ClipID != clipID {
			t.Fatalf("第 %d 条 clip_id = %d, 期望 %d（结果=%v）", i, got[i].ClipID, clipID, got)
		}
	}
}

// TestRankEmpty 校验空候选返回空结果。
func TestRankEmpty(t *testing.T) {
	if got := Rank(nil, nil, time.Now()); len(got) != 0 {
		t.Fatalf("空候选返回 %d 条", len(got))
	}
}
