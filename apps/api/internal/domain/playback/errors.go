// errors.go 定义 playback 领域的哨兵错误。
package domplayback

import "errors"

// ErrInvalidConfigUser 表示配置所属用户非法。
var ErrInvalidConfigUser = errors.New("invalid playback config user")

// ErrInvalidPreloadCount 表示预加载条数越界。
var ErrInvalidPreloadCount = errors.New("invalid preload count")

// ErrInvalidSampleRate 表示 QoS 采样率越界。
var ErrInvalidSampleRate = errors.New("invalid qos sample rate")

// ErrInvalidReportTarget 表示 QoS 上报的 reporter 或 clip 非法。
var ErrInvalidReportTarget = errors.New("invalid qos report target")

// ErrEventIDEmpty 表示幂等事件 ID 为空。
var ErrEventIDEmpty = errors.New("qos report event_id must not be empty")

// ErrInvalidMetric 表示 QoS 指标字段为负。
var ErrInvalidMetric = errors.New("invalid qos metric")

// ErrInvalidClientInfo 表示 client_info 长度超限。
var ErrInvalidClientInfo = errors.New("client_info too long")
