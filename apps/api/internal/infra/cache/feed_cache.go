// feed_cache.go 实现 Feed 页/卡片/计数三级缓存、互动状态缓存与关注流 inbox/outbox：
// TTL 加 10%–20% 抖动，key 版本前缀 pf:v1；客户端为 nil 时所有读写静默降级
// （Get 未命中、Set 跳过），由调用方回源 DB。
package cache

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
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
	// cardTTL 是卡片（clip 视图）缓存基础 TTL。
	cardTTL = 15 * time.Minute
	// statsTTL 是互动计数缓存基础 TTL：计数变化频繁，短 TTL 控制偏差。
	statsTTL = 15 * time.Second

	// actionShardCount 是互动计数分片数：按用户 ID 散列到分片，降低单键写入热点。
	actionShardCount = 16
	// actionStateAttempts 是 WATCH 事务在并发冲突下的最大重试次数。
	actionStateAttempts = 3

	// inboxMaxEntries 是 inbox/outbox 裁剪上限：只保留按分值（发布时间）最新的 N 条。
	inboxMaxEntries = 1000
)

// ErrActionStateUnavailable 表示 Redis 未启用，异步互动链路应回退同步落库。
var ErrActionStateUnavailable = errors.New("互动状态缓存不可用")

// ErrFeedBoxUnavailable 表示 Redis 未启用，following 场景应回源 DB。
var ErrFeedBoxUnavailable = errors.New("关注流缓存不可用")

// FeedCache 是 Feed 三级缓存的 Redis 实现；client 为 nil 表示缓存禁用。
type FeedCache struct {
	client *redis.Client
}

// NewFeedCache 创建 Feed 缓存；client 传 nil 时缓存禁用，全部操作降级。
func NewFeedCache(client *redis.Client) *FeedCache {
	return &FeedCache{client: client}
}

// Enabled 报告缓存是否启用。
func (c *FeedCache) Enabled() bool {
	return c != nil && c.client != nil
}

// GetPage 读取一页 Feed 条目缓存；未命中或缓存不可用时返回 ok=false。
func (c *FeedCache) GetPage(ctx context.Context, digest string) ([]*domfeed.FeedEntry, bool) {
	if !c.Enabled() {
		return nil, false
	}
	raw, err := c.client.Get(ctx, pageKey(digest)).Bytes()
	if err != nil {
		return nil, false
	}
	var entries []*domfeed.FeedEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		// 缓存内容损坏按未命中处理，回源后覆盖回填。
		return nil, false
	}
	return entries, true
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

// GetCards 批量读取卡片缓存；返回命中部分，未命中的 ID 由调用方回源（S11 起消费）。
func (c *FeedCache) GetCards(ctx context.Context, clipIDs []int64) map[int64]*domfeed.FeedEntry {
	found := make(map[int64]*domfeed.FeedEntry, len(clipIDs))
	if !c.Enabled() || len(clipIDs) == 0 {
		return found
	}
	keys := make([]string, 0, len(clipIDs))
	for _, id := range clipIDs {
		keys = append(keys, cardKey(id))
	}
	values, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		return found
	}
	for i, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		entry := &domfeed.FeedEntry{}
		if err := json.Unmarshal([]byte(raw), entry); err != nil {
			continue
		}
		found[clipIDs[i]] = entry
	}
	return found
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

// GetStats 批量读取互动计数缓存；返回命中部分（S9 起消费）。
func (c *FeedCache) GetStats(ctx context.Context, clipIDs []int64) map[int64]*domfeed.EntryStats {
	found := make(map[int64]*domfeed.EntryStats, len(clipIDs))
	if !c.Enabled() || len(clipIDs) == 0 {
		return found
	}
	keys := make([]string, 0, len(clipIDs))
	for _, id := range clipIDs {
		keys = append(keys, statsKey(id))
	}
	values, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		return found
	}
	for i, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		stats := &domfeed.EntryStats{}
		if err := json.Unmarshal([]byte(raw), stats); err != nil {
			continue
		}
		found[clipIDs[i]] = stats
	}
	return found
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

// ActionState 是单条互动状态快照，供批量回填缓存使用。
type ActionState struct {
	UserID     int64
	ClipID     int64
	ActionType dominteraction.ActionType
	Active     bool
}

// SetActionState 以 WATCH + 事务管道更新读者对某个 clip 的行为状态，
// 仅在状态真正变化时按用户散列累加/累减 16 分片计数；Redis 未启用时返回 ErrActionStateUnavailable。
func (c *FeedCache) SetActionState(ctx context.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool) (*dominteraction.ActionResult, error) {
	if !c.Enabled() {
		return nil, ErrActionStateUnavailable
	}
	if !actionType.Valid() {
		return nil, dominteraction.ErrInvalidActionType
	}

	stateKey := actionStateKey(clipID, userID, actionType)
	target := "0"
	if active {
		target = "1"
	}

	var lastErr error
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

			_, txErr := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, target, 0)
				delta := int64(0)
				switch {
				case active && !current:
					delta = 1
				case !active && exists && current:
					delta = -1
				}
				if delta != 0 {
					key := actionCountKey(clipID, actionShard(userID), actionType)
					if delta > 0 {
						pipe.IncrBy(ctx, key, delta)
					} else {
						pipe.DecrBy(ctx, key, -delta)
					}
				}
				return nil
			})
			return txErr
		}, stateKey)
		if errors.Is(err, redis.TxFailedErr) {
			// 并发写入同一状态键：重新 WATCH 后按最新状态重算增量。
			lastErr = err
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
			LikeCount:  likeCount,
			SaveCount:  saveCount,
		}, nil
	}
	return nil, fmt.Errorf("更新互动状态缓存冲突重试耗尽: %w", lastErr)
}

// BatchSetActionStates 批量写入行为状态（不调整计数），供离线/回放场景同步 Redis 快照。
func (c *FeedCache) BatchSetActionStates(ctx context.Context, states []ActionState) {
	if !c.Enabled() || len(states) == 0 {
		return
	}
	pipe := c.client.Pipeline()
	for _, state := range states {
		if !state.ActionType.Valid() {
			continue
		}
		value := "0"
		if state.Active {
			value = "1"
		}
		pipe.Set(ctx, actionStateKey(state.ClipID, state.UserID, state.ActionType), value, 0)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("warning: 批量写入互动状态缓存失败: %v", err)
	}
}

// sumActionCounts 汇总某个 clip 的点赞/收藏 16 分片计数。
func (c *FeedCache) sumActionCounts(ctx context.Context, clipID int64) (int64, int64, error) {
	keys := make([]string, 0, actionShardCount*2)
	for shard := 0; shard < actionShardCount; shard++ {
		keys = append(keys, actionCountKey(clipID, shard, dominteraction.ActionLike))
	}
	for shard := 0; shard < actionShardCount; shard++ {
		keys = append(keys, actionCountKey(clipID, shard, dominteraction.ActionSave))
	}

	values, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("读取互动计数缓存失败: %w", err)
	}
	like, save := int64(0), int64(0)
	for i, value := range values {
		text, ok := value.(string)
		if !ok || text == "" {
			continue
		}
		n, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr != nil {
			continue
		}
		if i < actionShardCount {
			like += n
		} else {
			save += n
		}
	}
	return like, save, nil
}

// AddInboxEntries 将条目推入读者 inbox（ZSET，score=published_at 秒），并裁剪到最近 1000 条；
// 缓存禁用或空集时静默跳过，由读路径回源 DB。
func (c *FeedCache) AddInboxEntries(ctx context.Context, userID int64, entries []domfeed.InboxEntry) error {
	return c.addFeedBoxEntries(ctx, inboxKey(userID), entries)
}

// ListInbox 读取读者 inbox 全部条目（≤1000）；缓存未启用返回 ErrFeedBoxUnavailable，由策略回源 DB。
func (c *FeedCache) ListInbox(ctx context.Context, userID int64) ([]domfeed.InboxEntry, error) {
	return c.listFeedBox(ctx, inboxKey(userID))
}

// AddOutboxEntries 将条目推入创作者 outbox（大 V 拉取模型），裁剪上限与 inbox 一致。
func (c *FeedCache) AddOutboxEntries(ctx context.Context, creatorID int64, entries []domfeed.InboxEntry) error {
	return c.addFeedBoxEntries(ctx, outboxKey(creatorID), entries)
}

// ListOutbox 读取创作者 outbox 全部条目（≤1000）；缓存未启用返回 ErrFeedBoxUnavailable。
func (c *FeedCache) ListOutbox(ctx context.Context, creatorID int64) ([]domfeed.InboxEntry, error) {
	return c.listFeedBox(ctx, outboxKey(creatorID))
}

// addFeedBoxEntries 是 inbox/outbox 写入的公共实现：ZADD 批量推入后按排名裁剪到最新 N 条。
func (c *FeedCache) addFeedBoxEntries(ctx context.Context, key string, entries []domfeed.InboxEntry) error {
	if !c.Enabled() || len(entries) == 0 {
		return nil
	}
	members := make([]redis.Z, 0, len(entries))
	for _, entry := range entries {
		members = append(members, redis.Z{
			Score:  float64(entry.PublishedAt.Unix()),
			Member: feedBoxMember(entry.ClipID, entry.CreatorID),
		})
	}
	pipe := c.client.Pipeline()
	pipe.ZAdd(ctx, key, members...)
	// 只保留按分值（发布时间）最新的 N 条：排名 0..-(N+1) 的都是较旧条目。
	pipe.ZRemRangeByRank(ctx, key, 0, -int64(inboxMaxEntries)-1)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("写入关注流缓存失败: %w", err)
	}
	return nil
}

// listFeedBox 是 inbox/outbox 读取的公共实现：整表读出（上限 N 条），合并排序由调用方完成。
func (c *FeedCache) listFeedBox(ctx context.Context, key string) ([]domfeed.InboxEntry, error) {
	if !c.Enabled() {
		return nil, ErrFeedBoxUnavailable
	}
	values, err := c.client.ZRevRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("读取关注流缓存失败: %w", err)
	}
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
		entries = append(entries, domfeed.InboxEntry{
			ClipID:      clipID,
			CreatorID:   creatorID,
			PublishedAt: time.Unix(int64(value.Score), 0),
		})
	}
	return entries, nil
}

// inboxKey 返回读者收件箱键。
func inboxKey(userID int64) string {
	return fmt.Sprintf("%s:feed:inbox:%d", keyPrefix, userID)
}

// outboxKey 返回创作者发件箱键。
func outboxKey(creatorID int64) string {
	return fmt.Sprintf("%s:feed:outbox:%d", keyPrefix, creatorID)
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

// pageKey 返回页缓存键；digest 由应用层按 sha1(scene|viewer|cursor|limit)[:16] 计算（C5）。
func pageKey(digest string) string {
	return keyPrefix + ":feed:page:" + digest
}

// cardKey 返回卡片缓存键。
func cardKey(clipID int64) string {
	return fmt.Sprintf("%s:feed:card:%d", keyPrefix, clipID)
}

// statsKey 返回计数缓存键。
func statsKey(clipID int64) string {
	return fmt.Sprintf("%s:feed:stats:%d", keyPrefix, clipID)
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
