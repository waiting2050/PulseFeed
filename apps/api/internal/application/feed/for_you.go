// for_you.go 实现 for-you 场景策略：调用推荐服务取排序后的 clip，按 ID 批量水合作者条目。
// 水合会丢弃已删除/转非发布态的条目，因此按推荐顺序向后补取候选直到补齐一页，
// has_more 改为水合后判定，游标锚定在已成功返回条目中排序序列的最后一条。
package appfeed

import (
	"context"
	"time"

	apprecommendation "pulsefeed/internal/application/recommendation"
	domfeed "pulsefeed/internal/domain/feed"
	domrecommendation "pulsefeed/internal/domain/recommendation"
)

// forYouBackfillRounds 是单次 for-you 请求读取推荐页的最大轮数：
// 首轮水合后被删除条目挤占的名额，允许再向后读一次候选（最多 2 轮）。
// 既保证「有内容就能翻到」，又避免单请求无限回源：最多 2 次 Recommend 与 2 轮水合
// （每轮 1 次 BatchGetEntries + 1 次 BatchGetStats）。
const forYouBackfillRounds = 2

// Recommender 是推荐读取端口，由 application/recommendation.Service 实现。
type Recommender interface {
	// Recommend 返回一页推荐结果，Items 含 clip ID 与分数，NextCursor 携带 snapshot_at。
	Recommend(ctx context.Context, viewerID int64, cursor *domfeed.Cursor, limit int) (*apprecommendation.Result, error)
}

// ForYouStrategy 是 for-you 场景策略：推荐排序 + 卡片/计数批量水合。
// 该场景个性化且依赖实时行为，声明不缓存，避免页缓存把某个读者的排序结果复用给他人。
type ForYouStrategy struct {
	repo        domfeed.Repository
	recommender Recommender
}

// forYouCandidate 是一条成功水合的推荐候选：entry 用于展示，item 保留排序键供生成游标。
type forYouCandidate struct {
	entry *domfeed.FeedEntry
	item  domrecommendation.RankedClip
}

// NewForYouStrategy 创建 for-you 场景策略。
func NewForYouStrategy(repo domfeed.Repository, recommender Recommender) *ForYouStrategy {
	return &ForYouStrategy{repo: repo, recommender: recommender}
}

// Scene 返回 for-you 场景标识。
func (s *ForYouStrategy) Scene() domfeed.Scene {
	return domfeed.SceneForYou
}

// Cacheable 报告 for-you 不使用页缓存。
func (s *ForYouStrategy) Cacheable() bool {
	return false
}

// Load 读取一页推荐条目：未登录返回 ErrViewerRequired，游标缺少 snapshot_at 返回 ErrInvalidCursor；
// 推荐结果按 ID 批量回源卡片与计数，已删除 clip 跳过并向后补取，直到补齐 limit 条或候选耗尽。
func (s *ForYouStrategy) Load(ctx context.Context, req *FeedRequest, cursor *domfeed.Cursor, count int) (*Page, error) {
	if req.ViewerID <= 0 {
		return nil, domfeed.ErrViewerRequired
	}
	if cursor != nil && cursor.SnapshotAt == nil {
		return nil, domfeed.ErrInvalidCursor
	}

	// count 比页大小多 1，这里只需要水合出 limit 条；has_more 由候选是否真的还有剩余决定。
	limit := count - 1
	if limit < 1 {
		limit = 1
	}

	var (
		candidates    []forYouCandidate
		next          = cursor
		snapshotAt    *time.Time
		recommendNext string
		moreAvailable bool
	)
	if cursor != nil {
		snapshotAt = cursor.SnapshotAt
	}

	for round := 0; round < forYouBackfillRounds && len(candidates) < limit; round++ {
		need := limit - len(candidates)
		result, err := s.recommender.Recommend(ctx, req.ViewerID, next, need)
		if err != nil {
			return nil, err
		}
		recommendNext = result.NextCursor
		moreAvailable = recommendNext != ""
		if len(result.Items) == 0 {
			moreAvailable = false
			break
		}
		hydrated, err := s.hydrate(ctx, result.Items)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, hydrated...)
		if !moreAvailable {
			break
		}
		decoded, err := domfeed.DecodeCursor(recommendNext)
		if err != nil {
			// 推荐服务给出的游标本应可解析：解析失败按候选耗尽处理，避免原地重试同一页。
			moreAvailable = false
			break
		}
		next = decoded
		if decoded.SnapshotAt != nil {
			snapshotAt = decoded.SnapshotAt
		}
	}

	page := &Page{Entries: make([]*domfeed.FeedEntry, 0, len(candidates))}
	for _, candidate := range candidates {
		page.Entries = append(page.Entries, candidate.entry)
	}
	// has_more 改为水合后判定：候选确实还有剩余才声明下一页，既不会因删条提前截断，
	// 也不会在候选耗尽时仍引导客户端继续翻页。
	if !moreAvailable {
		return page, nil
	}
	if len(candidates) == 0 || snapshotAt == nil {
		// 本页候选全部被水合丢弃（或拿不到快照）：直接沿用推荐服务的游标越过这段空洞。
		if recommendNext != "" {
			page.NextCursor = recommendNext
			page.HasMore = true
		}
		return page, nil
	}

	anchor := lastRecommendAnchor(candidates)
	snapshot := *snapshotAt
	page.NextCursor = domfeed.EncodeCursor(&domfeed.Cursor{
		PublishedAt: anchor.PublishedAt,
		ClipID:      anchor.ClipID,
		Score:       anchor.Score,
		SnapshotAt:  &snapshot,
	})
	page.HasMore = true
	return page, nil
}

// hydrate 按推荐顺序水合一页候选：批量回源卡片与计数，已删除 clip 跳过；
// 返回顺序与入参一致，保留推荐的创作者交错展示顺序。
func (s *ForYouStrategy) hydrate(ctx context.Context, items []domrecommendation.RankedClip) ([]forYouCandidate, error) {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ClipID)
	}
	cards, err := s.repo.BatchGetEntries(ctx, ids)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.BatchGetStats(ctx, ids)
	if err != nil {
		return nil, err
	}

	candidates := make([]forYouCandidate, 0, len(items))
	for _, item := range items {
		card, ok := cards[item.ClipID]
		if !ok {
			// clip 已删除或转非发布态：跳过，由补取继续向后覆盖。
			continue
		}
		entry := *card
		entry.PublishedAt = item.PublishedAt
		if value, ok := stats[item.ClipID]; ok {
			entry.Stats = *value
		}
		candidates = append(candidates, forYouCandidate{entry: &entry, item: item})
	}
	return candidates, nil
}

// lastRecommendAnchor 返回已成功返回条目中排序序列的最后一条（score、clip_id 最小者）。
// 展示顺序是按创作者交错的，不能直接取展示顺序的末条：交错后末条分值可能高于其他条目，
// 用它锚定会把尚未返回的高分候选永久跳过；取排序最小者覆盖了全部已返回条目，
// 下一页严格小于该键，既不重复也不跳条。
func lastRecommendAnchor(candidates []forYouCandidate) domrecommendation.RankedClip {
	anchor := candidates[0].item
	for _, candidate := range candidates[1:] {
		item := candidate.item
		if item.Score < anchor.Score || (item.Score == anchor.Score && item.ClipID < anchor.ClipID) {
			anchor = item
		}
	}
	return anchor
}
