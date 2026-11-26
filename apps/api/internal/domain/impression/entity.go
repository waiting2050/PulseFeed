// entity.go 定义展示领域的播放事件与展示聚合实体，以及事件 ID、事件类型、场景与时长的校验规则。
package domimpression

import (
	"strings"
	"time"

	domfeed "pulsefeed/internal/domain/feed"
)

const (
	// eventIDMaxLen 是播放事件 ID（幂等键）长度上限，与 pf_play_events 列宽一致。
	eventIDMaxLen = 128
	// requestIDMaxLen 是与请求批次关联的 request_id 长度上限，与 pf_play_events 列宽一致。
	requestIDMaxLen = 128
	// watchMsMaxMs 是单条事件上报的观看时长上限（毫秒），超过视为非法。
	watchMsMaxMs int64 = 24 * 60 * 60 * 1000
)

// EventType 是一次展示/播放事件的类型；exposed 只登记展示，play/complete/skip 记录后续行为。
type EventType string

const (
	// EventTypeExposed 表示卡片进入视口并完成一次展示。
	EventTypeExposed EventType = "exposed"
	// EventTypePlay 表示用户开始播放；只有该类型累加播放计数。
	EventTypePlay EventType = "play"
	// EventTypeComplete 表示播放到结尾。
	EventTypeComplete EventType = "complete"
	// EventTypeSkip 表示卡片展示后未播放即被划走。
	EventTypeSkip EventType = "skip"
)

// ParseEventType 解析事件类型：空值按旧客户端语义视为 play，其余只接受四种合法取值。
func ParseEventType(raw string) (EventType, error) {
	switch EventType(strings.TrimSpace(raw)) {
	case "":
		return EventTypePlay, nil
	case EventTypeExposed, EventTypePlay, EventTypeComplete, EventTypeSkip:
		return EventType(strings.TrimSpace(raw)), nil
	default:
		return "", ErrInvalidEventType
	}
}

// normalizeScene 校验场景取值：允许为空（旧客户端未上报场景），非空时必须是 Feed 支持的场景。
func normalizeScene(raw string) (string, error) {
	scene := strings.TrimSpace(raw)
	if scene == "" {
		return "", nil
	}
	if _, err := domfeed.ParseScene(scene); err != nil {
		return "", ErrInvalidScene
	}
	return scene, nil
}

// PlayEventInput 是创建播放事件的入参，字段与上报接口一一对应。
type PlayEventInput struct {
	EventID    string
	ViewerID   int64
	ClipID     int64
	EventType  string
	Scene      string
	RequestID  string
	WatchMs    int64
	Completed  bool
	Impression bool
}

// PlayEvent 是一次展示/播放行为的流水记录；同一 EventID 在全局只落一行，天然幂等。
type PlayEvent struct {
	EventID    string
	ViewerID   int64
	ClipID     int64
	EventType  EventType
	Scene      string
	RequestID  string
	WatchMs    int64
	Completed  bool
	Impression bool
	CreatedAt  time.Time
}

// NewPlayEvent 校验并创建一条播放事件：EventID 非空且限长、ClipID 为正、事件类型与场景合法、
// 观看时长非负且不超过单日上限。
func NewPlayEvent(in PlayEventInput) (*PlayEvent, error) {
	eventID := strings.TrimSpace(in.EventID)
	if eventID == "" || len(eventID) > eventIDMaxLen {
		return nil, ErrInvalidEventID
	}
	if in.ClipID <= 0 {
		return nil, ErrClipNotFound
	}

	eventType, err := ParseEventType(in.EventType)
	if err != nil {
		return nil, err
	}
	scene, err := normalizeScene(in.Scene)
	if err != nil {
		return nil, err
	}
	requestID := strings.TrimSpace(in.RequestID)
	if len(requestID) > requestIDMaxLen {
		return nil, ErrInvalidRequestID
	}
	if in.WatchMs < 0 || in.WatchMs > watchMsMaxMs {
		return nil, ErrInvalidPlayDuration
	}

	return &PlayEvent{
		EventID:    eventID,
		ViewerID:   in.ViewerID,
		ClipID:     in.ClipID,
		EventType:  eventType,
		Scene:      scene,
		RequestID:  requestID,
		WatchMs:    in.WatchMs,
		Completed:  in.Completed,
		Impression: in.Impression,
		CreatedAt:  time.Now(),
	}, nil
}

// Day 将时间截断到本地日（YYYY-MM-DD），作为展示聚合的分桶维度。
func Day(t time.Time) string {
	return t.Format("2006-01-02")
}

// Impression 是某读者在某日内对某条 clip 的聚合记录；唯一键为 (ViewerID, ClipID, Day)。
// PlayCount 只被 play 事件累加，CompletedCount 只被 complete 事件累加。
type Impression struct {
	ViewerID       int64
	ClipID         int64
	Day            string
	PlayCount      int64
	CompletedCount int64
	FirstPlayedAt  time.Time
	LastPlayedAt   time.Time
}
