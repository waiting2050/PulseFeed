// entity.go 定义曝光领域的播放事件与曝光聚合实体，以及事件 ID 与时长的校验规则。
package domimpression

import (
	"strings"
	"time"
)

const (
	// eventIDMaxLen 是播放事件 ID（幂等键）长度上限，与 pf_play_events 列宽一致。
	eventIDMaxLen = 128
	// playDurationMaxMs 是单条播放事件上报的观看时长上限（毫秒），超过视为非法。
	playDurationMaxMs int64 = 24 * 60 * 60 * 1000
)

// PlayEvent 是一次播放行为的流水记录；同一 EventID 在全局只落一行，天然幂等。
type PlayEvent struct {
	EventID        string
	ViewerID       int64
	ClipID         int64
	PlayDurationMs int64
	Completed      bool
	Impression     bool
	CreatedAt      time.Time
}

// NewPlayEvent 校验并创建一条播放事件：EventID 非空且限长、ClipID 为正、时长非负且不超过单日上限。
func NewPlayEvent(eventID string, viewerID, clipID int64, playDurationMs int64, completed, impression bool) (*PlayEvent, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" || len(eventID) > eventIDMaxLen {
		return nil, ErrInvalidEventID
	}
	if clipID <= 0 {
		return nil, ErrClipNotFound
	}
	if playDurationMs < 0 || playDurationMs > playDurationMaxMs {
		return nil, ErrInvalidPlayDuration
	}
	return &PlayEvent{
		EventID:        eventID,
		ViewerID:       viewerID,
		ClipID:         clipID,
		PlayDurationMs: playDurationMs,
		Completed:      completed,
		Impression:     impression,
		CreatedAt:      time.Now(),
	}, nil
}

// Day 将时间截断到本地日（YYYY-MM-DD），作为曝光聚合的分桶维度。
func Day(t time.Time) string {
	return t.Format("2006-01-02")
}

// Impression 是某读者在某日内对某条 clip 的曝光聚合；唯一键为 (ViewerID, ClipID, Day)。
type Impression struct {
	ViewerID       int64
	ClipID         int64
	Day            string
	PlayCount      int64
	CompletedCount int64
	FirstPlayedAt  time.Time
	LastPlayedAt   time.Time
}
