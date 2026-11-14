//go:build integration

// recommendation_exclusion_integration_test.go 验证 for-you 候选召回在 SQL 阶段
// 排除本人作品与已点赞/已收藏的作品，且排除发生在截断之前、不影响分页可用候选。
package integration

import (
	"context"
	"testing"
	"time"

	domclip "pulsefeed/internal/domain/clip"
	dominteraction "pulsefeed/internal/domain/interaction"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraaction "pulsefeed/internal/infra/persistence/interaction"
	infrarecommendation "pulsefeed/internal/infra/persistence/recommendation"
)

// TestListCandidatesExcludesOwnAndInteractedClips 验证三类候选排除：
// 本人作品、已点赞、已收藏均不进入候选；已取消的点赞不参与排除。
// 用例把发布时间放到快照前的高位并给有效候选较高互动计数，
// 保证候选排序不受其他用例残留数据（发布时间与既有热度）干扰。
func TestListCandidatesExcludesOwnAndInteractedClips(t *testing.T) {
	viewerID := int64(990001)
	otherCreatorID := int64(990002)
	// 其他集成用例写入的 clip 发布时间都早于当前时刻，用未来基准隔离本条用例。
	base := time.Now().Add(24 * time.Hour)
	snapshotAt := time.Now().Add(48 * time.Hour)

	ownClip := seedClip(t, viewerID, "rec-own", base.Add(5*time.Minute), domclip.StatusPublished)
	likedClip := seedClip(t, otherCreatorID, "rec-liked", base.Add(4*time.Minute), domclip.StatusPublished)
	savedClip := seedClip(t, otherCreatorID, "rec-saved", base.Add(3*time.Minute), domclip.StatusPublished)
	canceledClip := seedClip(t, otherCreatorID, "rec-canceled", base.Add(2*time.Minute), domclip.StatusPublished)
	cleanClip := seedClip(t, otherCreatorID, "rec-clean", base.Add(time.Minute), domclip.StatusPublished)

	seedCandidateAction(t, viewerID, likedClip, dominteraction.ActionLike, dominteraction.StatusActive)
	seedCandidateAction(t, viewerID, savedClip, dominteraction.ActionSave, dominteraction.StatusActive)
	seedCandidateAction(t, viewerID, canceledClip, dominteraction.ActionLike, dominteraction.StatusCanceled)

	// 给两条有效候选较高的互动计数：热度排序下把它们稳定放到候选池最前，
	// 避免其他用例中已有互动的 clip 抢占限量名额（排序列是 hot_score DESC）。
	seedCandidateStats(t, canceledClip, 100)
	seedCandidateStats(t, cleanClip, 50)

	repo := infrarecommendation.New(testDB)
	candidates, err := repo.ListCandidates(context.Background(), viewerID, snapshotAt, 50)
	if err != nil {
		t.Fatalf("候选召回失败: %v", err)
	}
	found := make(map[int64]bool, len(candidates))
	for _, candidate := range candidates {
		found[candidate.ClipID] = true
	}

	for name, clipID := range map[string]int64{
		"本人作品": ownClip,
		"已点赞":  likedClip,
		"已收藏":  savedClip,
	} {
		if found[clipID] {
			t.Fatalf("%s 的 clip %d 不应进入 for-you 候选", name, clipID)
		}
	}
	for name, clipID := range map[string]int64{
		"已取消点赞": canceledClip,
		"无互动":   cleanClip,
	} {
		if !found[clipID] {
			t.Fatalf("%s 的 clip %d 应保留在 for-you 候选中", name, clipID)
		}
	}

	// 排除必须发生在 LIMIT 之前：热度相同按发布时间倒序，前三条是应被排除的作品，
	// 只取 2 条时仍应返回两条有效候选，而不是被排除项占满后返回空页。
	limited, err := repo.ListCandidates(context.Background(), viewerID, snapshotAt, 2)
	if err != nil {
		t.Fatalf("限量候选召回失败: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("限量候选数 = %d, 期望 2（排除项不应占用候选额度）", len(limited))
	}
	if limited[0].ClipID != canceledClip || limited[1].ClipID != cleanClip {
		t.Fatalf("限量候选 = [%d %d], 期望 [%d %d]", limited[0].ClipID, limited[1].ClipID, canceledClip, cleanClip)
	}
}

// seedCandidateStats 写入一条 clip 计数，用于把本用例候选稳定排在候选池前列。
func seedCandidateStats(t *testing.T, clipID, likeCount int64) {
	t.Helper()
	model := &infraclip.ClipStatsModel{ClipID: clipID, LikeCount: likeCount}
	if err := testDB.Create(model).Error; err != nil {
		t.Fatalf("写入 clip 计数失败: %v", err)
	}
}

// seedCandidateAction 写入一条点赞/收藏行为，供候选排除断言使用。
func seedCandidateAction(t *testing.T, userID, clipID int64, actionType dominteraction.ActionType, status int) {
	t.Helper()
	model := &infraaction.ActionModel{
		UserID:         userID,
		ClipID:         clipID,
		ActionType:     string(actionType),
		Status:         status,
		IdempotencyKey: uniqueKey("candidate"),
	}
	if err := testDB.Create(model).Error; err != nil {
		t.Fatalf("写入互动行为失败: %v", err)
	}
}
