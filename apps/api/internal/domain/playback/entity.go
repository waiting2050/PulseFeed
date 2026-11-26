// entity.go 定义 playback 领域的实体与值对象：播放配置与 QoS 上报。
package domplayback

import "time"

// PreloadCountDefault 是预加载条数的默认值；客户端未显式设置时用它。
const PreloadCountDefault = 8

// PreloadCountMax 是预加载条数上限，防止客户端请求过多。
const PreloadCountMax = 32

// QoSSampleRateDefault 是 QoS 采样率默认值；表示比例，取 [0, 1]。
const QoSSampleRateDefault = 0.1

// QoSSampleRateMax 是采样率上限，避免上报压力过大。
const QoSSampleRateMax = 1.0

// BufferMSDefault 是播放缓冲目标时长的默认值（毫秒）；短视频场景 2 秒足以覆盖起播抖动。
const BufferMSDefault = 2000

// BufferMSMin 是缓冲时长下限（毫秒），避免客户端把目标压到无法稳定起播。
const BufferMSMin = 500

// BufferMSMax 是缓冲时长上限（毫秒），避免客户端缓存过多占用带宽与内存。
const BufferMSMax = 10000

// Config 是用户级播放配置；每个用户一行，含预加载条数、QoS 采样率与缓冲目标时长。
type Config struct {
	UserID        int64
	PreloadCount  int
	QoSSampleRate float64
	BufferMS      int
	UpdatedAt     time.Time
}

// Normalize 把配置字段夹取到合法区间；零值或缺失值替换为默认。
func (c *Config) Normalize() {
	if c.PreloadCount <= 0 {
		c.PreloadCount = PreloadCountDefault
	} else if c.PreloadCount > PreloadCountMax {
		c.PreloadCount = PreloadCountMax
	}
	if c.QoSSampleRate <= 0 {
		c.QoSSampleRate = QoSSampleRateDefault
	} else if c.QoSSampleRate > QoSSampleRateMax {
		c.QoSSampleRate = QoSSampleRateMax
	}
	switch {
	case c.BufferMS <= 0:
		c.BufferMS = BufferMSDefault
	case c.BufferMS < BufferMSMin:
		c.BufferMS = BufferMSMin
	case c.BufferMS > BufferMSMax:
		c.BufferMS = BufferMSMax
	}
}

// PreloadMedia 是预加载队列中的一条媒体信息，只暴露客户端预取所需的公开地址与时长。
type PreloadMedia struct {
	ClipID     int64
	MediaURL   string
	CoverURL   string
	DurationMS int64
}

// QoSReport 是一次 QoS 上报；同一 event_id 重复上报幂等。
type QoSReport struct {
	ReporterID int64
	ClipID     int64
	EventID    string
	LatencyMS  int
	StallCount int
	Completed  bool
	ClientInfo string
	CreatedAt  time.Time
}

// Validate 校验 QoSReport 必填字段；event_id 为空时返回 ErrEventIDEmpty。
func (r *QoSReport) Validate() error {
	if r.ReporterID <= 0 || r.ClipID <= 0 {
		return ErrInvalidReportTarget
	}
	if r.EventID == "" {
		return ErrEventIDEmpty
	}
	if r.LatencyMS < 0 || r.StallCount < 0 {
		return ErrInvalidMetric
	}
	if len(r.ClientInfo) > 256 {
		return ErrInvalidClientInfo
	}
	return nil
}
