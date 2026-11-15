// feed_cache.go 实现 Feed 页/卡片/计数三级缓存、互动状态缓存与关注流 inbox/outbox：
// TTL 加 10%–20% 抖动；页/卡片/计数键版本前缀 pf:v1，inbox/outbox 因分值改为毫秒独立使用 pf:v2；
// 客户端为 nil 时所有读写静默降级（Get 未命中、Set 跳过），由调用方回源 DB。
package cache

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	domfeed "pulsefeed/internal/domain/feed"
	dominteraction "pulsefeed/internal/domain/interaction"

	"github.com/redis/go-redis/v9"
)

const (
	// keyPrefix 是全部缓存键的版本前缀，结构升级时整体换版本即可失效旧键。
	keyPrefix = "pf:v1"

	// firstPageTTL 是首页缓存基础 TTL：热点页短 TTL 保证内容新鲜。
	firstPageTTL = 5 * time.Second
	// pagedTTL 是翻页缓存基础 TTL：翻页流量低、内容相对稳定，TTL 更长。
	pagedTTL = 45 * time.Second
	// emptyPageTTL 是空结果页的负标记 TTL：同一 digest 回源得到空页后，短时间内重复请求
	// 直接返回空页而不再回源。取 3s 的依据：覆盖单次页面加载内的重复预取/重试窗口，
	// 又远短于翻页 TTL 45s，让新内容在最坏情况下只被空结论挡住 3s，
	// 避免「无更多内容」这一短暂结论按长 TTL 固化。
	emptyPageTTL = 3 * time.Second
	// emptyPageMarker 是负标记的存储内容；条目页总是 JSON 数组，读到该串即视为命中空页。
	emptyPageMarker = "__empty__"
	// cardTTL 是卡片（clip 视图）缓存基础 TTL。
	cardTTL = 15 * time.Minute
	// statsTTL 是互动计数缓存基础 TTL：计数变化频繁，短 TTL 控制偏差。
	statsTTL = 15 * time.Second
	// missingCardTTL 是卡片/计数负占位的 TTL：回源确认 clip 不存在后，短时间内不再重复回源。
	// 取 30s 的依据：略长于 statsTTL(15s)，可覆盖连续翻页/客户端重试造成的重复读取；
	// 又远短于 cardTTL(15min)，误判最多 30s 自愈。发布/互动路径会用真实数据覆盖同键，
	// 且 clip ID 不复用，因此负占位不会挡住正常新发布内容的可见性。
	missingCardTTL = 30 * time.Second
	// missingMarker 是卡片/计数负占位的存储内容，与 JSON 结构区分。
	missingMarker = "__missing__"

	// actionShardCount 是互动计数分片数：按用户 ID 散列到分片，降低单键写入热点。
	actionShardCount = 16
	// actionStateAttempts 是 WATCH 事务在并发冲突下的最大重试次数。
	actionStateAttempts = 3
	// actionSeedAttempts 是计数基线种子化时 WATCH 快照冲突的最大重试次数。
	actionSeedAttempts = 3

	// actionCountTTL 是互动计数基线键与分片键的有效期：24 小时后按 pf_clip_stats 重新种子化，
	// 避免长期偏移与冷数据常驻；写入时基线随分片一起续期。
	actionCountTTL = 24 * time.Hour

	// actionBaseFieldLike / actionBaseFieldSave 是计数基线 hash 的两个字段。
	actionBaseFieldLike = "like"
	actionBaseFieldSave = "save"

	// feedBoxKeyPrefix 是 inbox/outbox 专用的键版本前缀。
	// 分值从秒改为毫秒后，pf:v1 键里的旧分值按毫秒解读会整体前移约 50 年，
	// 与毫秒分值的游标边界完全错位；因此只给 inbox/outbox 升到 pf:v2，
	// 不动物理上无关的页/卡片/计数键。旧键不再被读写，随 7 天 TTL 自然过期；
	// 过渡期内新键尚未建立，读侧会回源 DB 补齐（fallbackFollowingPage），不会 500。
	feedBoxKeyPrefix = "pf:v2"

	// inboxMaxEntries 是 inbox/outbox 裁剪上限：只保留按分值（发布时间）最新的 N 条。
	inboxMaxEntries = 1000
	// outboxPipelineBatch 是单次 outbox 批量读取的 pipeline 分片命令数上限：
	// 关注 100 个大 V 时一次往返即可读完全部源；极端关注量下按每批 100 个键分片，
	// 往返数仍从 O(N) 降到 O(N/100)。
	outboxPipelineBatch = 100
	// feedBoxTTL 是 inbox/outbox 的写入侧续期时长：每次 fanout/回填写入都会重新计时，
	// 长期没有新内容的键自然过期，避免无界增长；选择写入续期而不是定时对账重建，
	// 是因为键可由数据库重新扇出重建，且回源补齐保证了过期后读侧仍然正确。
	feedBoxTTL = 7 * 24 * time.Hour

	// hotWindowBuckets 是 hot 场景合并的分钟桶数量（最近 60 个桶，即约 1 小时）。
	hotWindowBuckets = 60
	// hotBucketTTL 是单个分钟桶的保留时长，略大于窗口跨度以便跨桶合并时尾部桶仍然可读。
	hotBucketTTL = 65 * time.Minute
	// hotWindowLimit 是合并窗口后参与排序的 clip 上限，防止超热时段单次读取过大。
	hotWindowLimit = 500

	// feedVersionDefault 是 Feed 页缓存版本缺失时的默认值。
	feedVersionDefault = "0"
)

// ErrActionStateUnavailable 表示 Redis 未启用，异步互动链路应回退同步落库。
var ErrActionStateUnavailable = errors.New("互动状态缓存不可用")

// ErrFeedBoxUnavailable 表示 Redis 未启用，following 场景应回源 DB。
var ErrFeedBoxUnavailable = errors.New("关注流缓存不可用")

// ErrHotWindowUnavailable 表示 Redis 未启用，hot 场景应回源 DB。
var ErrHotWindowUnavailable = errors.New("热度窗口缓存不可用")

// FeedCache 是 Feed 三级缓存的 Redis 实现；client 为 nil 表示缓存禁用。
type FeedCache struct {
	client *redis.Client
	// now 返回当前时间；为 nil 时使用 time.Now，测试可注入固定时钟验证分钟桶翻转。
	now func() time.Time
	// actionStats 提供 pf_clip_stats 基线，用于互动计数缓存的首次种子化与 TTL 重种子化。
	actionStats ActionStatsReader
}

// ActionStatsReader 是互动计数种子化的 DB 基线读取端口，由 feed 仓储实现。
type ActionStatsReader interface {
	BatchGetStats(ctx context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, error)
}

// FeedCacheOption 用于装配 FeedCache 的可选依赖。
type FeedCacheOption func(*FeedCache)

// WithActionStatsReader 注入互动计数基线读取端口；未注入时基线按 0 种子化。
func WithActionStatsReader(reader ActionStatsReader) FeedCacheOption {
	return func(c *FeedCache) { c.actionStats = reader }
}

// NewFeedCache 创建 Feed 缓存；client 传 nil 时缓存禁用，全部操作降级。
func NewFeedCache(client *redis.Client, opts ...FeedCacheOption) *FeedCache {
	c := &FeedCache{client: client}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Enabled 报告缓存是否启用。
func (c *FeedCache) Enabled() bool {
	return c != nil && c.client != nil
}

// clock 返回当前时间；未注入时钟时回退到 time.Now，生产行为保持不变。
func (c *FeedCache) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// GetPage 读取一页 Feed 条目缓存；ok=false 表示未命中，err 非 nil 表示缓存故障。
// 故障与未命中必须区分：调用方据此决定回源后是否回填，避免把故障当成「没有数据」。
func (c *FeedCache) GetPage(ctx context.Context, digest string) ([]*domfeed.FeedEntry, bool, error) {
	if !c.Enabled() {
		return nil, false, nil
	}
	raw, err := c.client.Get(ctx, pageKey(digest)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		log.Printf("warning: 读取 feed 页缓存失败 (digest=%s): %v", digest, err)
		return nil, false, err
	}
	// 负标记表示此前回源确认该 digest 没有内容：命中即返回空页，避免重复回源。
	if string(raw) == emptyPageMarker {
		return nil, true, nil
	}
	var entries []*domfeed.FeedEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		// 缓存内容损坏按未命中处理，回源后覆盖回填。
		log.Printf("warning: feed 页缓存内容损坏，按未命中处理 (digest=%s): %v", digest, err)
		return nil, false, nil
	}
	return entries, true, nil
}

// SetPage 回填一页 Feed 条目；firstPage 决定使用首页还是翻页 TTL，失败仅记日志不阻塞主链路。
func (c *FeedCache) SetPage(ctx context.Context, digest string, entries []*domfeed.FeedEntry, firstPage bool) {
	if !c.Enabled() {
		return
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		log.Printf("warning: 序列化 feed 页缓存失败: %v", err)
		return
	}
	ttl := pagedTTL
	if firstPage {
		ttl = firstPageTTL
	}
	if err := c.client.Set(ctx, pageKey(digest), raw, jitter(ttl)).Err(); err != nil {
		log.Printf("warning: 回填 feed 页缓存失败: %v", err)
	}
}

// SetPageEmpty 为空结果页写入短 TTL 负标记；失败仅记日志，由下次回源兜底。
// 负标记不区分首屏/翻页：空结论本身时效性极短，统一用 emptyPageTTL 是最保守的选择。
func (c *FeedCache) SetPageEmpty(ctx context.Context, digest string) {
	if !c.Enabled() {
		return
	}
	if err := c.client.Set(ctx, pageKey(digest), emptyPageMarker, jitter(emptyPageTTL)).Err(); err != nil {
		log.Printf("warning: 回填空页负标记失败 (digest=%s): %v", digest, err)
	}
}

// FeedVersion 返回当前 Feed 页缓存版本；缓存禁用或版本键尚未创建时返回默认值。
// 读取故障返回错误而不是退化为默认版本：否则摘要会落到版本 0，可能命中删除前的旧页。
// 版本号参与页缓存 digest，BumpFeedVersion 后旧页缓存不再命中，无需按 key 枚举删除。
func (c *FeedCache) FeedVersion(ctx context.Context) (string, error) {
	if !c.Enabled() {
		return feedVersionDefault, nil
	}
	version, err := c.client.Get(ctx, feedVersionKey()).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return feedVersionDefault, nil
		}
		log.Printf("warning: 读取 feed 页缓存版本失败: %v", err)
		return "", err
	}
	if version == "" {
		return feedVersionDefault, nil
	}
	return version, nil
}

// BumpFeedVersion 递增 Feed 页缓存版本，使既有页缓存整体失效；失败仅记日志不阻塞主链路。
func (c *FeedCache) BumpFeedVersion(ctx context.Context) {
	if !c.Enabled() {
		return
	}
	if err := c.client.Incr(ctx, feedVersionKey()).Err(); err != nil {
		log.Printf("warning: 递增 feed 页缓存版本失败: %v", err)
	}
}

// DeleteClipCache 删除单个 clip 的卡片与计数缓存；失败仅记日志，由 TTL 兜底。
func (c *FeedCache) DeleteClipCache(ctx context.Context, clipID int64) {
	if !c.Enabled() || clipID <= 0 {
		return
	}
	if err := c.client.Del(ctx, cardKey(clipID), statsKey(clipID)).Err(); err != nil {
		log.Printf("warning: 删除 clip 缓存失败 (clip_id=%d): %v", clipID, err)
	}
}

// GetCards 批量读取卡片缓存；返回值只含命中的 ID，未命中由调用方回源。
// negative 是带负占位的 ID（此前回源确认 DB 不存在），调用方应直接跳过而不是再次回源。
// err 非 nil 表示缓存故障，调用方必须把整批 ID 回源，不能按「未命中」处理。
func (c *FeedCache) GetCards(ctx context.Context, clipIDs []int64) (map[int64]*domfeed.FeedEntry, map[int64]bool, error) {
	found := make(map[int64]*domfeed.FeedEntry, len(clipIDs))
	negative := make(map[int64]bool, len(clipIDs))
	if !c.Enabled() || len(clipIDs) == 0 {
		return found, negative, nil
	}
	keys := make([]string, 0, len(clipIDs))
	for _, id := range clipIDs {
		keys = append(keys, cardKey(id))
	}
	values, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		log.Printf("warning: 读取卡片缓存失败 (count=%d): %v", len(clipIDs), err)
		return nil, nil, err
	}
	for i, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		if raw == missingMarker {
			negative[clipIDs[i]] = true
			continue
		}
		entry := &domfeed.FeedEntry{}
		if err := json.Unmarshal([]byte(raw), entry); err != nil {
			continue
		}
		found[clipIDs[i]] = entry
	}
	return found, negative, nil
}

// SetCards 批量回填卡片缓存；失败仅记日志（S11 起消费）。
func (c *FeedCache) SetCards(ctx context.Context, entries []*domfeed.FeedEntry) {
	if !c.Enabled() || len(entries) == 0 {
		return
	}
	pipe := c.client.Pipeline()
	for _, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		pipe.Set(ctx, cardKey(entry.ClipID), raw, jitter(cardTTL))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("warning: 回填卡片缓存失败: %v", err)
	}
}

// MarkCardsMissing 为回源确认不存在的 clip 写短 TTL 负占位，避免 inbox/outbox 残留的僵尸条目
// 每次读取都重复回源；写入失败仅记日志，由下次回源兜底。
func (c *FeedCache) MarkCardsMissing(ctx context.Context, clipIDs []int64) {
	c.markMissing(ctx, clipIDs, cardKey)
}

// GetStats 批量读取互动计数缓存；返回值只含命中的 ID，未命中由调用方回源。
// negative 是带负占位的 ID（此前回源确认 DB 不存在），调用方应直接跳过而不是再次回源。
// err 非 nil 表示缓存故障，调用方必须把整批 ID 回源，不能按「未命中」处理。
func (c *FeedCache) GetStats(ctx context.Context, clipIDs []int64) (map[int64]*domfeed.EntryStats, map[int64]bool, error) {
	found := make(map[int64]*domfeed.EntryStats, len(clipIDs))
	negative := make(map[int64]bool, len(clipIDs))
	if !c.Enabled() || len(clipIDs) == 0 {
		return found, negative, nil
	}
	keys := make([]string, 0, len(clipIDs))
	for _, id := range clipIDs {
		keys = append(keys, statsKey(id))
	}
	values, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		log.Printf("warning: 读取计数缓存失败 (count=%d): %v", len(clipIDs), err)
		return nil, nil, err
	}
	for i, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		if raw == missingMarker {
			negative[clipIDs[i]] = true
			continue
		}
		stats := &domfeed.EntryStats{}
		if err := json.Unmarshal([]byte(raw), stats); err != nil {
			continue
		}
		found[clipIDs[i]] = stats
	}
	return found, negative, nil
}

// MarkStatsMissing 为回源确认不存在的计数写短 TTL 负占位；写入失败仅记日志。
func (c *FeedCache) MarkStatsMissing(ctx context.Context, clipIDs []int64) {
	c.markMissing(ctx, clipIDs, statsKey)
}

// markMissing 用 pipeline 批量写入负占位，键名由 keyFn 决定（卡片或计数）。
func (c *FeedCache) markMissing(ctx context.Context, clipIDs []int64, keyFn func(int64) string) {
	if !c.Enabled() || len(clipIDs) == 0 {
		return
	}
	pipe := c.client.Pipeline()
	for _, clipID := range clipIDs {
		pipe.Set(ctx, keyFn(clipID), missingMarker, jitter(missingCardTTL))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("warning: 回填负占位失败 (count=%d): %v", len(clipIDs), err)
	}
}

// SetStats 批量回填互动计数缓存；失败仅记日志（S9 起消费）。
func (c *FeedCache) SetStats(ctx context.Context, stats map[int64]*domfeed.EntryStats) {
	if !c.Enabled() || len(stats) == 0 {
		return
	}
	pipe := c.client.Pipeline()
	for clipID, s := range stats {
		raw, err := json.Marshal(s)
		if err != nil {
			continue
		}
		pipe.Set(ctx, statsKey(clipID), raw, jitter(statsTTL))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("warning: 回填计数缓存失败: %v", err)
	}
}

// SetActionState 以 WATCH + 事务管道更新读者对某个 clip 的行为状态，
// 仅在状态真正变化时按用户散列累加/累减 16 分片增量；返回「DB 基线 + 分片增量」的最新计数。
// Redis 未启用时返回 ErrActionStateUnavailable，由调用方回退同步落库。
func (c *FeedCache) SetActionState(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool) (*dominteraction.ActionResult, error) {
	if !c.Enabled() {
		return nil, ErrActionStateUnavailable
	}
	if !actionType.Valid() {
		return nil, dominteraction.ErrInvalidActionType
	}

	// 首次访问或基线 TTL 到期时，用 pf_clip_stats 种子化 base 键；分片只存增量。
	if err := c.ensureActionBase(ctx, clipID); err != nil {
		return nil, err
	}

	stateKey := actionStateKey(clipID, userID, actionType)
	target := "0"
	if active {
		target = "1"
	}

	// 取消操作的下限保护：写入前先取一次当前合计，合计已为 0 时只翻转状态位、不再递减分片。
	// 多读者并发下这个判断是近似的，最终返回值仍由 sumActionCounts 钳制为非负。
	likeBefore, saveBefore, err := c.sumActionCounts(ctx, clipID)
	if err != nil {
		return nil, err
	}

	var lastErr error
	changed := false
	for attempt := 0; attempt < actionStateAttempts; attempt++ {
		err := c.client.Watch(ctx, func(tx *redis.Tx) error {
			raw, getErr := tx.Get(ctx, stateKey).Result()
			switch {
			case getErr == nil:
			case errors.Is(getErr, redis.Nil):
				raw = ""
			default:
				return getErr
			}
			exists := raw != ""
			current := raw == "1"
			if exists && current == active {
				// 状态未变化：无需写事务，幂等返回当前计数。
				return nil
			}

			delta := int64(0)
			switch {
			case active && !current:
				delta = 1
			case !active && exists && current:
				delta = -1
			}
			if delta < 0 {
				currentTotal := likeBefore
				if actionType == dominteraction.ActionSave {
					currentTotal = saveBefore
				}
				if currentTotal <= 0 {
					// 计数已到 0：取消只翻转状态位，分片不再下探为负数。
					delta = 0
				}
			}

			_, txErr := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, target, 0)
				countKey := actionCountKey(clipID, actionShard(userID), actionType)
				if delta != 0 {
					if delta > 0 {
						pipe.IncrBy(ctx, countKey, delta)
					} else {
						pipe.DecrBy(ctx, countKey, -delta)
					}
					// 写入即续期分片增量，与基线一起在 24 小时后重新种子化。
					pipe.Expire(ctx, countKey, actionCountTTL)
				}
				// 任何状态写入都续期基线，避免 base 先于分片过期后读数只覆盖老快照。
				pipe.Expire(ctx, actionBaseKey(clipID), actionCountTTL)
				return nil
			})
			if txErr != nil {
				return txErr
			}
			changed = true
			return nil
		}, stateKey)
		if errors.Is(err, redis.TxFailedErr) {
			// 并发写入同一状态键：重新 WATCH 后按最新状态重算增量。
			lastErr = err
			changed = false
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("更新互动状态缓存失败: %w", err)
		}

		likeCount, saveCount, err := c.sumActionCounts(ctx, clipID)
		if err != nil {
			return nil, err
		}
		return &dominteraction.ActionResult{
			ClipID:     clipID,
			ActionType: actionType,
			Active:     active,
			Changed:    changed,
			LikeCount:  likeCount,
			SaveCount:  saveCount,
		}, nil
	}
	return nil, fmt.Errorf("更新互动状态缓存冲突重试耗尽: %w", lastErr)
}

// sumActionCounts 返回 clip 当前可见的点赞/收藏计数：
// base 基线（pf_clip_stats 种子值或重种子值）+ 16 分片增量，两类结果各自钳制为非负。
func (c *FeedCache) sumActionCounts(ctx context.Context, clipID int64) (int64, int64, error) {
	baseKey := actionBaseKey(clipID)
	values, err := c.client.HMGet(ctx, baseKey, actionBaseFieldLike, actionBaseFieldSave).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("读取互动计数基线失败: %w", err)
	}
	if values[0] == nil && values[1] == nil {
		// 基线在读取瞬间缺失（TTL 到期或被删除）：先重种子化，避免只拿分片增量当总数。
		if err := c.ensureActionBase(ctx, clipID); err != nil {
			return 0, 0, err
		}
		if values, err = c.client.HMGet(ctx, baseKey, actionBaseFieldLike, actionBaseFieldSave).Result(); err != nil {
			return 0, 0, fmt.Errorf("读取互动计数基线失败: %w", err)
		}
	}

	likeSum, err := sumActionShards(ctx, c.client, clipID, dominteraction.ActionLike)
	if err != nil {
		return 0, 0, err
	}
	saveSum, err := sumActionShards(ctx, c.client, clipID, dominteraction.ActionSave)
	if err != nil {
		return 0, 0, err
	}

	like := parseCountValue(values[0]) + likeSum
	save := parseCountValue(values[1]) + saveSum
	if like < 0 {
		like = 0
	}
	if save < 0 {
		save = 0
	}
	return like, save, nil
}

// ensureActionBase 确保 clip 的计数基线键存在，缺失时按「库种子 + 增量」重新种子化。
//
// 种子公式：base[kind] = pf_clip_stats[kind] - Σ(shards[kind])，读路径返回 base + Σ(shards)。
// 这个公式同时避开两种错误：
//   - 不重复计：已经由 Worker 落库的增量既在 stats 里、也还留在分片里；把 Σ 从 stats 中减掉写进 base，
//     两条来源只保留 stats 这一份。若直接把 base 写成 stats（旧实现的读法），再叠加分片就会多算已落库部分。
//   - 不丢增量：种子化不删除、不清零任何分片，只调整 base；尚未由 Worker 落库、只存在于 Redis 的增量
//     原样保留在分片中（并在落库后的下一次重种子化时由 stats 吸收），不会像「清空分片」那样丢弃。
//
// 并发一致性：stats 在 Redis 之外读取，无法放进 MULTI。因此先 WATCH 基线与全部分片键，
// 在 WATCH 事务内重新求和分片并读取 stats；提交前任一相关键被改动都会触发 TxFailedErr 重试，
// 保证 base 使用的分片快照与写入时刻一致。分片写入先于 MQ 投递、MQ 投递先于 Worker 落库，
// 所以「先求和分片、后读 stats」时读到的 stats 一定覆盖分片增量，base 不会把已落库部分算重。
func (c *FeedCache) ensureActionBase(ctx context.Context, clipID int64) error {
	if !c.Enabled() {
		return ErrActionStateUnavailable
	}
	baseKey := actionBaseKey(clipID)
	exists, err := c.client.Exists(ctx, baseKey).Result()
	if err != nil {
		return fmt.Errorf("检查互动计数基线失败: %w", err)
	}
	if exists == 1 {
		return nil
	}

	shardKeys := make([]string, 0, actionShardCount*2)
	for shard := 0; shard < actionShardCount; shard++ {
		shardKeys = append(shardKeys, actionCountKey(clipID, shard, dominteraction.ActionLike))
	}
	for shard := 0; shard < actionShardCount; shard++ {
		shardKeys = append(shardKeys, actionCountKey(clipID, shard, dominteraction.ActionSave))
	}
	watchKeys := append([]string{baseKey}, shardKeys...)

	var lastErr error
	for attempt := 0; attempt < actionSeedAttempts; attempt++ {
		err := c.client.Watch(ctx, func(tx *redis.Tx) error {
			exists, err := tx.Exists(ctx, baseKey).Result()
			if err != nil {
				return err
			}
			if exists == 1 {
				// 并发的其它请求已完成种子化，无需重复写。
				return nil
			}
			likeSum, err := sumActionShards(ctx, tx, clipID, dominteraction.ActionLike)
			if err != nil {
				return err
			}
			saveSum, err := sumActionShards(ctx, tx, clipID, dominteraction.ActionSave)
			if err != nil {
				return err
			}
			stats, err := c.loadActionStats(ctx, clipID)
			if err != nil {
				return err
			}
			_, txErr := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.HSetNX(ctx, baseKey, actionBaseFieldLike, strconv.FormatInt(stats.LikeCount-likeSum, 10))
				pipe.HSetNX(ctx, baseKey, actionBaseFieldSave, strconv.FormatInt(stats.SaveCount-saveSum, 10))
				pipe.Expire(ctx, baseKey, actionCountTTL)
				// 分片与基线同时起算 TTL，避免分片先过期导致「base 还在但增量已丢」的读数偏低。
				for _, key := range shardKeys {
					pipe.Expire(ctx, key, actionCountTTL)
				}
				return nil
			})
			return txErr
		}, watchKeys...)
		if errors.Is(err, redis.TxFailedErr) {
			lastErr = err
			continue
		}
		if err != nil {
			return fmt.Errorf("互动计数基线种子化失败: %w", err)
		}
		return nil
	}
	// 冲突重试耗尽：若并发的其它请求已写好基线则视为成功，否则返回错误让调用方回退同步落库。
	if exists, existsErr := c.client.Exists(ctx, baseKey).Result(); existsErr == nil && exists == 1 {
		return nil
	}
	return fmt.Errorf("互动计数基线种子化冲突重试耗尽: %w", lastErr)
}

// loadActionStats 读取 clip 的 pf_clip_stats 基线；未注入读取端口或计数行缺失时按 0 处理。
func (c *FeedCache) loadActionStats(ctx context.Context, clipID int64) (domfeed.EntryStats, error) {
	if c.actionStats == nil {
		return domfeed.EntryStats{}, nil
	}
	found, err := c.actionStats.BatchGetStats(ctx, []int64{clipID})
	if err != nil {
		return domfeed.EntryStats{}, fmt.Errorf("读取 pf_clip_stats 基线失败: %w", err)
	}
	if stats, ok := found[clipID]; ok && stats != nil {
		return *stats, nil
	}
	return domfeed.EntryStats{}, nil
}

// countKeyReader 是分片求和所需的最小命令集合，*redis.Client 与 *redis.Tx 均满足。
type countKeyReader interface {
	MGet(ctx context.Context, keys ...string) *redis.SliceCmd
}

// sumActionShards 汇总某个 clip 单类行为（like/save）的 16 分片增量。
func sumActionShards(ctx context.Context, reader countKeyReader, clipID int64, actionType dominteraction.ActionType) (int64, error) {
	keys := make([]string, 0, actionShardCount)
	for shard := 0; shard < actionShardCount; shard++ {
		keys = append(keys, actionCountKey(clipID, shard, actionType))
	}
	values, err := reader.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("读取互动计数分片失败: %w", err)
	}
	var total int64
	for _, value := range values {
		total += parseCountValue(value)
	}
	return total, nil
}

// parseCountValue 把 Redis 返回值解析为计数；缺失或非法一律按 0 处理。
func parseCountValue(value any) int64 {
	text, ok := value.(string)
	if !ok || text == "" {
		return 0
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// RecordHotScore 按 clip ID 与当前分钟把热度增量累加到分钟桶 ZSET（ZINCRBY）：
// 桶键形如 pf:v1:feed:hot:202601021504（分钟内若干次小增量合并为桶内累计值）。
// delta 允许为负（取消点赞/收藏），零值不写桶。
func (c *FeedCache) RecordHotScore(ctx context.Context, clipID int64, delta float64) {
	if !c.Enabled() || clipID <= 0 || delta == 0 {
		return
	}
	key := hotBucketKey(c.clock())
	if err := c.client.ZIncrBy(ctx, key, delta, strconv.FormatInt(clipID, 10)).Err(); err != nil {
		log.Printf("warning: 热度分钟桶写入失败 (clip_id=%d): %v", clipID, err)
		return
	}
	if err := c.client.Expire(ctx, key, hotBucketTTL).Err(); err != nil {
		log.Printf("warning: 热度分钟桶 TTL 设置失败 (clip_id=%d): %v", clipID, err)
	}
}

// ListHotWindow 合并最近 hotWindowBuckets 个分钟桶（一次 pipeline 往返读完全部桶），
// 返回按累计热度递减排序的 HotItem 列表。Redis 未启用时返回 ErrHotWindowUnavailable，
// 由 hot 策略回源 DB。
func (c *FeedCache) ListHotWindow(ctx context.Context) ([]domfeed.HotItem, error) {
	if !c.Enabled() {
		return nil, ErrHotWindowUnavailable
	}

	// 取最近 N 个分钟桶，统一格式化为 yyyyMMddHHmm（分钟精度）。
	now := c.clock()
	keys := make([]string, 0, hotWindowBuckets)
	for i := 0; i < hotWindowBuckets; i++ {
		keys = append(keys, hotBucketKey(now.Add(-time.Duration(i)*time.Minute)))
	}

	// 内存合并：用 pipeline 一次往返读完 N 个分钟桶，逐桶累计各 clip 增量，之后统一排序并截断。
	// 串行 ZRANGE 会让单次热榜请求产生 N 次 Redis 往返；pipeline 只保留 1 次。
	pipe := c.client.Pipeline()
	cmds := make([]*redis.ZSliceCmd, 0, len(keys))
	for _, key := range keys {
		cmds = append(cmds, pipe.ZRangeWithScores(ctx, key, 0, -1))
	}
	// Exec 只返回首个命令错误，部分桶失败（如类型错误）时仍需逐条检查结果，
	// 因此这里不因整体错误提前返回，失败按命令计数以保持原有部分失败语义。
	_, execErr := pipe.Exec(ctx)
	if execErr != nil {
		log.Printf("warning: 热度分钟桶 pipeline 读取返回错误 (buckets=%d): %v", len(keys), execErr)
	}

	acc := make(map[string]float64, hotWindowLimit)
	failed := 0
	for i, cmd := range cmds {
		values, err := cmd.Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			log.Printf("warning: 热度分钟桶读取失败 (key=%s): %v", keys[i], err)
			failed++
			continue
		}
		for _, value := range values {
			member, ok := value.Member.(string)
			if !ok {
				continue
			}
			acc[member] += value.Score
		}
	}
	// 连接级故障（Redis 不可达）只会挂在 Exec 上，逐条命令拿不到错误也没有结果；
	// 此时按全部桶失败处理，避免把「读不到」误判成空窗口。
	if execErr != nil && failed == 0 {
		failed = len(keys)
	}
	// 全部桶读取失败，或部分失败且没有任何数据时，无法判断窗口是真的为空还是读失败：
	// 返回错误让上层按缓存故障回源 DB，而不是记成 empty_window。
	if failed == len(keys) || (failed > 0 && len(acc) == 0) {
		return nil, fmt.Errorf("%w: %d/%d 个分钟桶读取失败", ErrHotWindowUnavailable, failed, len(keys))
	}

	items := make([]domfeed.HotItem, 0, len(acc))
	for member, score := range acc {
		// 取消点赞/收藏会写入负分；累计后非正分的条目不再占用热榜位置。
		if score <= 0 {
			continue
		}
		clipID, err := strconv.ParseInt(member, 10, 64)
		if err != nil {
			continue
		}
		items = append(items, domfeed.HotItem{ClipID: clipID, Score: score})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ClipID > items[j].ClipID
	})
	if len(items) > hotWindowLimit {
		items = items[:hotWindowLimit]
	}
	return items, nil
}

// AddInboxEntries 将条目推入读者 inbox（ZSET，score=published_at 秒），并裁剪到最近 1000 条；
// 缓存禁用或空集时静默跳过，由读路径回源 DB。
func (c *FeedCache) AddInboxEntries(ctx context.Context, userID int64, entries []domfeed.InboxEntry) error {
	return c.addFeedBoxEntries(ctx, inboxKey(userID), entries)
}

// ListInbox 读取读者 inbox 中游标之后的至多 limit 条条目；cursor 为 nil 表示首屏。
// 只按需取一页，避免把整个键（≤1000 条）读回内存；缓存未启用返回 ErrFeedBoxUnavailable。
func (c *FeedCache) ListInbox(ctx context.Context, userID int64, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error) {
	return c.listFeedBox(ctx, inboxKey(userID), cursor, limit)
}

// RemoveCreatorFromInbox 删除读者 inbox 中指定创作者的全部残留条目（取关清理）。
// 成员是 clipID:creatorID 复合串，无法按下标或分值范围定位，只能用 ZSCAN MATCH "*:<creatorID>"
// 扫描出精确成员后再批量 ZREM；代价是 O(N)（N≤inboxMaxEntries=1000）的读与至多 10 次小批量删除。
// 取关是低频写操作，这比额外维护 creator→clipID 反查索引更简单，也不会引入双写不一致。
// 失败返回错误由调用方记日志，不影响取关结果：读路径仍按实时关注集过滤兜底。
func (c *FeedCache) RemoveCreatorFromInbox(ctx context.Context, userID, creatorID int64) error {
	if !c.Enabled() || userID <= 0 || creatorID <= 0 {
		return nil
	}
	key := inboxKey(userID)
	suffix := ":" + strconv.FormatInt(creatorID, 10)

	// 先完整扫描再统一删除：若在 ZSCAN 迭代过程中同步 ZREM，哈希表收缩会让游标跳过元素，
	// 导致部分残留清理不掉（实测 240 条只能清掉约 140 条）。inbox 上限 1000，
	// 把命中成员暂存在内存里是一次有界的 O(N) 读，换取清理的完整性。
	matched := make([]interface{}, 0, 100)
	var scanCursor uint64
	for {
		members, next, err := c.client.ZScan(ctx, key, scanCursor, "*"+suffix, 500).Result()
		if err != nil {
			return fmt.Errorf("扫描关注流缓存失败: %w", err)
		}
		// ZSCAN 返回 member/score 交替的扁平数组，只需 member。
		for i := 0; i+1 < len(members); i += 2 {
			matched = append(matched, members[i])
		}
		scanCursor = next
		if scanCursor == 0 {
			break
		}
	}

	// ZREM 必须带精确 member；按 100 个一批删除，避免单条命令参数过大。
	for start := 0; start < len(matched); start += 100 {
		end := start + 100
		if end > len(matched) {
			end = len(matched)
		}
		if err := c.client.ZRem(ctx, key, matched[start:end]...).Err(); err != nil {
			return fmt.Errorf("清理关注流缓存失败: %w", err)
		}
	}
	return nil
}

// AddOutboxEntries 将条目推入创作者 outbox（大 V 拉取模型），裁剪上限与 inbox 一致。
func (c *FeedCache) AddOutboxEntries(ctx context.Context, creatorID int64, entries []domfeed.InboxEntry) error {
	return c.addFeedBoxEntries(ctx, outboxKey(creatorID), entries)
}

// ListOutboxes 批量读取多个创作者 outbox 中游标之后的至多 limit 条条目：
// 所有读取命令合并进一次 pipeline（超过 outboxPipelineBatch 时按批分片），
// 把 N 个源的往返数从 O(N) 压到 O(N/100)，因此调用方无需再为 outbox 单独限并发。
func (c *FeedCache) ListOutboxes(ctx context.Context, creatorIDs []int64, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error) {
	if !c.Enabled() {
		return nil, ErrFeedBoxUnavailable
	}
	if limit <= 0 {
		limit = 1
	}
	entries := make([]domfeed.InboxEntry, 0, len(creatorIDs)*limit)
	for start := 0; start < len(creatorIDs); start += outboxPipelineBatch {
		end := start + outboxPipelineBatch
		if end > len(creatorIDs) {
			end = len(creatorIDs)
		}
		batch := creatorIDs[start:end]
		pipe := c.client.Pipeline()
		cmds := make([]*redis.ZSliceCmd, 0, len(batch))
		for _, creatorID := range batch {
			cmds = append(cmds, queueFeedBoxRead(ctx, pipe, outboxKey(creatorID), cursor, limit))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, fmt.Errorf("批量读取关注流缓存失败: %w", err)
		}
		for _, cmd := range cmds {
			values, err := cmd.Result()
			if err != nil {
				return nil, fmt.Errorf("批量读取关注流缓存失败: %w", err)
			}
			entries = append(entries, decodeFeedBoxValues(values, cursor, limit)...)
		}
	}
	return entries, nil
}

// addFeedBoxEntries 是 inbox/outbox 写入的公共实现：ZADD 批量推入后按排名裁剪到最新 N 条。
func (c *FeedCache) addFeedBoxEntries(ctx context.Context, key string, entries []domfeed.InboxEntry) error {
	if !c.Enabled() || len(entries) == 0 {
		return nil
	}
	members := make([]redis.Z, 0, len(entries))
	for _, entry := range entries {
		members = append(members, redis.Z{
			// 毫秒分值：同一秒内的多条内容不再共用一个分值，
			// 游标推进到某一毫秒后无需重读整秒。
			Score:  float64(entry.PublishedAt.UnixMilli()),
			Member: feedBoxMember(entry.ClipID, entry.CreatorID),
		})
	}
	pipe := c.client.Pipeline()
	pipe.ZAdd(ctx, key, members...)
	// 只保留按分值（发布时间）最新的 N 条：排名 0..-(N+1) 的都是较旧条目。
	pipe.ZRemRangeByRank(ctx, key, 0, -int64(inboxMaxEntries)-1)
	// 写入即续期：活跃键保持可用，沉寂键到期后由第 3 步的 DB 回源兜住读路径。
	pipe.Expire(ctx, key, jitter(feedBoxTTL))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("写入关注流缓存失败: %w", err)
	}
	return nil
}

// listFeedBox 是 inbox/outbox 单键读取的实现：与批量读取共用同一命令构造与解码逻辑，
// 只是把一条命令放进管道执行，保证单键与批量语义完全一致。
func (c *FeedCache) listFeedBox(ctx context.Context, key string, cursor *domfeed.Cursor, limit int) ([]domfeed.InboxEntry, error) {
	if !c.Enabled() {
		return nil, ErrFeedBoxUnavailable
	}
	if limit <= 0 {
		limit = 1
	}

	pipe := c.client.Pipeline()
	cmd := queueFeedBoxRead(ctx, pipe, key, cursor, limit)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("读取关注流缓存失败: %w", err)
	}
	values, err := cmd.Result()
	if err != nil {
		return nil, fmt.Errorf("读取关注流缓存失败: %w", err)
	}
	return decodeFeedBoxValues(values, cursor, limit), nil
}

// queueFeedBoxRead 把一次 inbox/outbox 读取命令加入管道并返回结果命令：
// 首屏按排名取最新 limit 条；翻页按毫秒分值用 ZREVRANGEBYSCORE 取游标及更旧的 limit+1 条。
func queueFeedBoxRead(ctx context.Context, pipe redis.Pipeliner, key string, cursor *domfeed.Cursor, limit int) *redis.ZSliceCmd {
	if cursor == nil {
		return pipe.ZRevRangeWithScores(ctx, key, 0, int64(limit-1))
	}
	// 分值已到毫秒，同值窗口只剩同一毫秒内的条目，不再需要 ZCOUNT 补同秒配额：
	// 多取一条留给复合边界过滤（游标自身最多占一条）即可。
	maxScore := strconv.FormatInt(cursor.PublishedAt.UnixMilli(), 10)
	return pipe.ZRevRangeByScoreWithScores(ctx, key, &redis.ZRangeBy{
		Max:    maxScore,
		Min:    "-inf",
		Offset: 0,
		Count:  int64(limit + 1),
	})
}

// decodeFeedBoxValues 把 ZSET 结果解码为轻量条目，并按 (published_at, clip_id) 复合边界过滤，
// 保持与数据库分页一致的游标语义（排除游标本身及同一毫秒内已读条目）。
func decodeFeedBoxValues(values []redis.Z, cursor *domfeed.Cursor, limit int) []domfeed.InboxEntry {
	entries := make([]domfeed.InboxEntry, 0, len(values))
	for _, value := range values {
		member, ok := value.Member.(string)
		if !ok {
			continue
		}
		clipID, creatorID, ok := parseFeedBoxMember(member)
		if !ok {
			continue
		}
		publishedAt := time.UnixMilli(int64(value.Score))
		// 同一毫秒内仍可能有多条；用 clip_id 复合边界排除游标本身及已读条目。
		if cursor != nil && !publishedAt.Before(cursor.PublishedAt) {
			if publishedAt.After(cursor.PublishedAt) || clipID >= cursor.ClipID {
				continue
			}
		}
		entries = append(entries, domfeed.InboxEntry{
			ClipID:      clipID,
			CreatorID:   creatorID,
			PublishedAt: publishedAt,
		})
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// inboxKey 返回读者收件箱键。
func inboxKey(userID int64) string {
	return fmt.Sprintf("%s:feed:inbox:%d", feedBoxKeyPrefix, userID)
}

// outboxKey 返回创作者发件箱键。
func outboxKey(creatorID int64) string {
	return fmt.Sprintf("%s:feed:outbox:%d", feedBoxKeyPrefix, creatorID)
}

// feedBoxMember 编码 inbox/outbox 条目成员：clipID:creatorID 复合串，
// 读路径据此按创作者过滤/归属，无需先回源卡片。
func feedBoxMember(clipID, creatorID int64) string {
	return strconv.FormatInt(clipID, 10) + ":" + strconv.FormatInt(creatorID, 10)
}

// parseFeedBoxMember 解析条目成员；非法成员返回 ok=false，由调用方跳过。
func parseFeedBoxMember(member string) (int64, int64, bool) {
	parts := strings.Split(member, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	clipID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	creatorID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return clipID, creatorID, true
}

// actionStateKey 返回单个读者行为状态键。
func actionStateKey(clipID, userID int64, actionType dominteraction.ActionType) string {
	return fmt.Sprintf("%s:action:state:%d:%d:%s", keyPrefix, clipID, userID, actionType)
}

// actionBaseKey 返回某个 clip 的互动计数基线 hash 键：字段 like/save 保存「库种子 - 分片增量」。
func actionBaseKey(clipID int64) string {
	return fmt.Sprintf("%s:action:base:%d", keyPrefix, clipID)
}

// actionCountKey 返回按用户散列分片后的计数键。
func actionCountKey(clipID int64, shard int, actionType dominteraction.ActionType) string {
	return fmt.Sprintf("%s:action:count:%d:%s:%d", keyPrefix, clipID, actionType, shard)
}

// actionShard 把用户 ID 映射到固定分片，同一读者的加减始终落在同一分片。
func actionShard(userID int64) int {
	if userID < 0 {
		userID = -userID
	}
	return int(userID % actionShardCount)
}

// pageKey 返回页缓存键；digest 由应用层按 sha1(scene|viewer|cursor|limit|version)[:16] 计算。
func pageKey(digest string) string {
	return keyPrefix + ":feed:page:" + digest
}

// feedVersionKey 返回 Feed 页缓存版本键；版本号单调递增，用于整体失效既有页缓存。
func feedVersionKey() string {
	return keyPrefix + ":feed:version"
}

// cardKey 返回卡片缓存键。
func cardKey(clipID int64) string {
	return fmt.Sprintf("%s:feed:card:%d", keyPrefix, clipID)
}

// statsKey 返回计数缓存键。
func statsKey(clipID int64) string {
	return fmt.Sprintf("%s:feed:stats:%d", keyPrefix, clipID)
}

// hotBucketKey 返回按分钟切分的热度桶键：同一分钟内的热度增量累加在同一个 ZSET。
func hotBucketKey(t time.Time) string {
	return fmt.Sprintf("%s:feed:hot:%s", keyPrefix, t.Format("200601021504"))
}

// jitter 给基础 TTL 叠加 10%–20% 的随机正向抖动，避免大量键同刻过期引发击穿；
// 随机源失败时退化为固定 +15%。
func jitter(base time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(11))
	if err != nil {
		return base * 115 / 100
	}
	return base * time.Duration(110+n.Int64()) / 100
}
