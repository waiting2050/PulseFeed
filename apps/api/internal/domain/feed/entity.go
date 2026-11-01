// entity.go 定义 feed 领域的场景枚举、Feed 条目、翻页游标编解码与热度公式。
package domfeed

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"time"
)

const (
	// MaxFeedLimit 是 Feed 单页条数上限。
	MaxFeedLimit = 50
	// DefaultFeedLimit 是未显式指定 limit 时的默认页大小。
	DefaultFeedLimit = 20
)

// Scene 是 Feed 场景标识；S7 支持 latest，S11 接入 following，其余场景在后续阶段接入。
type Scene string

const (
	// SceneLatest 按发布时间严格递减的全站最新 Feed。
	SceneLatest Scene = "latest"
	// SceneFollowing 仅含当前读者关注创作者的 Feed（inbox + 大 V outbox 合并，S11 接入）。
	SceneFollowing Scene = "following"
)

// ParseScene 解析场景字符串；不支持的场景返回 ErrUnsupportedScene。
func ParseScene(s string) (Scene, error) {
	switch Scene(s) {
	case SceneLatest:
		return SceneLatest, nil
	case SceneFollowing:
		return SceneFollowing, nil
	default:
		return "", ErrUnsupportedScene
	}
}

// EntryStats 是 Feed 条目的互动计数，随 clip 发布创建为 0 值行。
type EntryStats struct {
	LikeCount    int64
	SaveCount    int64
	CommentCount int64
	PlayCount    int64
}

// ViewerState 是当前读者对条目的互动状态，未登录读者恒为 false。
type ViewerState struct {
	Liked bool
	Saved bool
}

// FeedEntry 是 Feed 流中的一条条目：clip 视图 + 互动计数 + 读者互动状态。
type FeedEntry struct {
	ClipID      int64
	CreatorID   int64
	Title       string
	VideoURL    string
	CoverURL    string
	PublishedAt time.Time
	Stats       EntryStats
	Viewer      ViewerState
}

// InboxEntry 是关注流收件箱/发件箱中的轻量条目，仅携带排序与归属字段；
// 完整卡片与计数由组装阶段经卡片/计数缓存与批量回源补齐。
type InboxEntry struct {
	ClipID      int64
	CreatorID   int64
	PublishedAt time.Time
}

// FollowedCreator 是读者关注中的一名创作者及其粉丝数，供大 V outbox 判定。
type FollowedCreator struct {
	CreatorID     int64
	FollowerCount int64
}

// Cursor 是按 (published_at, clip_id) 复合键严格递减翻页的游标；
// 推荐场景的 snapshot_at 修正（D6）在 S14 扩展该结构。
type Cursor struct {
	PublishedAt time.Time `json:"p"`
	ClipID      int64     `json:"c"`
}

// EncodeCursor 将游标编码为 URL 安全字符串；nil 游标编码为空串。
func EncodeCursor(c *Cursor) string {
	if c == nil {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor 解析游标字符串；空串返回 nil 表示首页，非法串返回 ErrInvalidCursor。
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	cursor := &Cursor{}
	if err := json.Unmarshal(raw, cursor); err != nil {
		return nil, ErrInvalidCursor
	}
	return cursor, nil
}

// ScoreHot 计算 clip 热度分：互动计数按行为加权后，按发布时间的小时年龄做幂衰减。
// S15 hot 场景按该公式回源排序；权重与衰减指数随 S15 验收固定。
func ScoreHot(stats EntryStats, publishedAt, now time.Time) float64 {
	weight := float64(stats.LikeCount*3 + stats.SaveCount*4 + stats.CommentCount*5 + stats.PlayCount)
	ageHours := now.Sub(publishedAt).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	return weight / math.Pow(ageHours+2, 1.5)
}
