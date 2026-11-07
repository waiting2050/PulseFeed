// playback_test.go 用内存 fake 仓储验证播放配置默认值（preload_count=8、qos_sample_rate=0.1）、
// 更新回读、预加载最新 clip_id 列表，以及 QoS 同 event_id 幂等、不同 event 累加指标。
package appplayback_test

import (
	"context"
	"errors"
	"testing"

	appplayback "pulsefeed/internal/application/playback"
	domplayback "pulsefeed/internal/domain/playback"
)

// playbackFake 是内存版 playback 仓储，按领域端口语义保存配置与 QoS 上报。
type playbackFake struct {
	configs       map[int64]*domplayback.Config
	qosEvents     map[string]struct{}
	latencyTotal  int
	stallTotal    int
	completedRows int
	qosRows       int
}

// newPlaybackFake 构造空的内存仓储。
func newPlaybackFake() *playbackFake {
	return &playbackFake{
		configs:   map[int64]*domplayback.Config{},
		qosEvents: map[string]struct{}{},
	}
}

// GetOrCreateConfig 未命中时返回归一化后的默认配置（不落库）。
func (f *playbackFake) GetOrCreateConfig(_ context.Context, userID int64) (*domplayback.Config, error) {
	if userID <= 0 {
		return nil, domplayback.ErrInvalidConfigUser
	}
	if stored, ok := f.configs[userID]; ok {
		cfg := *stored
		cfg.Normalize()
		return &cfg, nil
	}
	cfg := &domplayback.Config{UserID: userID}
	cfg.Normalize()
	return cfg, nil
}

// UpsertConfig 写入并返回归一化后的配置副本。
func (f *playbackFake) UpsertConfig(_ context.Context, cfg *domplayback.Config) (*domplayback.Config, error) {
	if cfg == nil || cfg.UserID <= 0 {
		return nil, domplayback.ErrInvalidConfigUser
	}
	stored := *cfg
	stored.Normalize()
	f.configs[cfg.UserID] = &stored
	result := stored
	return &result, nil
}

// CreateQoS 按 event_id 幂等插入，仅新事件累加延迟/卡顿/完播指标。
func (f *playbackFake) CreateQoS(_ context.Context, report *domplayback.QoSReport) (bool, error) {
	if report == nil {
		return false, domplayback.ErrInvalidReportTarget
	}
	if err := report.Validate(); err != nil {
		return false, err
	}
	if _, exists := f.qosEvents[report.EventID]; exists {
		return false, nil
	}
	f.qosEvents[report.EventID] = struct{}{}
	f.qosRows++
	f.latencyTotal += report.LatencyMS
	f.stallTotal += report.StallCount
	if report.Completed {
		f.completedRows++
	}
	return true, nil
}

// playbackFakeClips 是内存版预加载数据源，按传入顺序返回最新的 limit 条媒体信息。
type playbackFakeClips struct {
	items    []domplayback.PreloadMedia
	gotCall  int
	gotLimit int
}

// ListLatestMedia 模拟按 (published_at, id) 递减的最新列表，最多返回 limit 条。
func (c *playbackFakeClips) ListLatestMedia(_ context.Context, limit int) ([]domplayback.PreloadMedia, error) {
	c.gotCall++
	c.gotLimit = limit
	if limit > len(c.items) {
		limit = len(c.items)
	}
	result := make([]domplayback.PreloadMedia, limit)
	copy(result, c.items[:limit])
	return result, nil
}

// TestPlaybackDefaultConfig 验证未设置配置时返回 preload_count=8、qos_sample_rate=0.1。
func TestPlaybackDefaultConfig(t *testing.T) {
	service := appplayback.New(newPlaybackFake(), nil)

	cfg, err := service.GetConfig(context.Background(), 42)
	if err != nil {
		t.Fatalf("读取默认配置失败: %v", err)
	}
	if cfg.PreloadCount != 8 {
		t.Fatalf("默认 preload_count 期望 8，实际 %d", cfg.PreloadCount)
	}
	if cfg.QoSSampleRate != 0.1 {
		t.Fatalf("默认 qos_sample_rate 期望 0.1，实际 %v", cfg.QoSSampleRate)
	}
	if cfg.BufferMS != 2000 {
		t.Fatalf("默认 buffer_ms 期望 2000，实际 %d", cfg.BufferMS)
	}
}

// TestPlaybackUpsertConfigReadBack 验证更新配置后可读回，零值被归一化为默认。
func TestPlaybackUpsertConfigReadBack(t *testing.T) {
	service := appplayback.New(newPlaybackFake(), nil)
	ctx := context.Background()

	written, err := service.UpsertConfig(ctx, 42, 12, 0.25, 3000)
	if err != nil {
		t.Fatalf("更新配置失败: %v", err)
	}
	if written.PreloadCount != 12 || written.QoSSampleRate != 0.25 || written.BufferMS != 3000 {
		t.Fatalf("返回落库值不符: %+v", written)
	}

	read, err := service.GetConfig(ctx, 42)
	if err != nil {
		t.Fatalf("读回配置失败: %v", err)
	}
	if read.PreloadCount != 12 || read.QoSSampleRate != 0.25 || read.BufferMS != 3000 {
		t.Fatalf("读回配置不符: preload=%d sample=%v buffer=%d", read.PreloadCount, read.QoSSampleRate, read.BufferMS)
	}

	normalized, err := service.UpsertConfig(ctx, 42, 0, 0, 0)
	if err != nil {
		t.Fatalf("更新零值配置失败: %v", err)
	}
	if normalized.PreloadCount != 8 || normalized.QoSSampleRate != 0.1 || normalized.BufferMS != 2000 {
		t.Fatalf("零值配置应归一化为默认值，实际 preload=%d sample=%v buffer=%d", normalized.PreloadCount, normalized.QoSSampleRate, normalized.BufferMS)
	}
}

// intPtr 与 floatPtr 便于构造部分更新请求的可选字段。
func intPtr(v int) *int { return &v }

func floatPtr(v float64) *float64 { return &v }

// TestPlaybackPartialUpdateKeepsOtherField 验证只传一个字段时另一字段保留库中现网值。
func TestPlaybackPartialUpdateKeepsOtherField(t *testing.T) {
	service := appplayback.New(newPlaybackFake(), nil)
	ctx := context.Background()

	if _, err := service.UpsertConfig(ctx, 42, 12, 0.25, 3000); err != nil {
		t.Fatalf("准备配置失败: %v", err)
	}

	onlyPreload, err := service.UpdateConfig(ctx, 42, intPtr(20), nil, nil)
	if err != nil {
		t.Fatalf("只更新预加载条数失败: %v", err)
	}
	if onlyPreload.PreloadCount != 20 || onlyPreload.QoSSampleRate != 0.25 || onlyPreload.BufferMS != 3000 {
		t.Fatalf("只更新预加载条数后应为 20/0.25/3000，实际 %d/%v/%d", onlyPreload.PreloadCount, onlyPreload.QoSSampleRate, onlyPreload.BufferMS)
	}

	onlySample, err := service.UpdateConfig(ctx, 42, nil, floatPtr(0.5), nil)
	if err != nil {
		t.Fatalf("只更新采样率失败: %v", err)
	}
	if onlySample.PreloadCount != 20 || onlySample.QoSSampleRate != 0.5 || onlySample.BufferMS != 3000 {
		t.Fatalf("只更新采样率后应为 20/0.5/3000，实际 %d/%v/%d", onlySample.PreloadCount, onlySample.QoSSampleRate, onlySample.BufferMS)
	}

	onlyBuffer, err := service.UpdateConfig(ctx, 42, nil, nil, intPtr(5000))
	if err != nil {
		t.Fatalf("只更新缓冲时长失败: %v", err)
	}
	if onlyBuffer.PreloadCount != 20 || onlyBuffer.QoSSampleRate != 0.5 || onlyBuffer.BufferMS != 5000 {
		t.Fatalf("只更新缓冲时长后应为 20/0.5/5000，实际 %d/%v/%d", onlyBuffer.PreloadCount, onlyBuffer.QoSSampleRate, onlyBuffer.BufferMS)
	}

	unchanged, err := service.UpdateConfig(ctx, 42, nil, nil, nil)
	if err != nil {
		t.Fatalf("三个字段都不传失败: %v", err)
	}
	if unchanged.PreloadCount != 20 || unchanged.QoSSampleRate != 0.5 || unchanged.BufferMS != 5000 {
		t.Fatalf("三个字段都不传应保留现有值 20/0.5/5000，实际 %d/%v/%d", unchanged.PreloadCount, unchanged.QoSSampleRate, unchanged.BufferMS)
	}
}

// TestPlaybackConfigBoundary 验证越界与临界配置按现有规则夹取或回落默认值。
func TestPlaybackConfigBoundary(t *testing.T) {
	cases := []struct {
		name        string
		preload     int
		sample      float64
		buffer      int
		wantPreload int
		wantSample  float64
		wantBuffer  int
	}{
		{name: "preload 33", preload: 33, sample: 0.5, buffer: 2000, wantPreload: 32, wantSample: 0.5, wantBuffer: 2000},
		{name: "preload 0", preload: 0, sample: 0.5, buffer: 2000, wantPreload: 8, wantSample: 0.5, wantBuffer: 2000},
		{name: "preload negative", preload: -3, sample: 0.5, buffer: 2000, wantPreload: 8, wantSample: 0.5, wantBuffer: 2000},
		{name: "sample 33", preload: 5, sample: 33, buffer: 2000, wantPreload: 5, wantSample: 1, wantBuffer: 2000},
		{name: "sample 0", preload: 5, sample: 0, buffer: 2000, wantPreload: 5, wantSample: 0.1, wantBuffer: 2000},
		{name: "sample negative", preload: 5, sample: -0.5, buffer: 2000, wantPreload: 5, wantSample: 0.1, wantBuffer: 2000},
		{name: "sample 0.005", preload: 5, sample: 0.005, buffer: 2000, wantPreload: 5, wantSample: 0.005, wantBuffer: 2000},
		{name: "buffer 0", preload: 5, sample: 0.5, buffer: 0, wantPreload: 5, wantSample: 0.5, wantBuffer: 2000},
		{name: "buffer below min", preload: 5, sample: 0.5, buffer: 100, wantPreload: 5, wantSample: 0.5, wantBuffer: 500},
		{name: "buffer above max", preload: 5, sample: 0.5, buffer: 60000, wantPreload: 5, wantSample: 0.5, wantBuffer: 10000},
		{name: "buffer boundary", preload: 5, sample: 0.5, buffer: 10000, wantPreload: 5, wantSample: 0.5, wantBuffer: 10000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := appplayback.New(newPlaybackFake(), nil)
			cfg, err := service.UpsertConfig(context.Background(), 44, tc.preload, tc.sample, tc.buffer)
			if err != nil {
				t.Fatalf("写入边界配置失败: %v", err)
			}
			if cfg.PreloadCount != tc.wantPreload || cfg.QoSSampleRate != tc.wantSample || cfg.BufferMS != tc.wantBuffer {
				t.Fatalf("期望 %d/%v/%d，实际 %d/%v/%d", tc.wantPreload, tc.wantSample, tc.wantBuffer, cfg.PreloadCount, cfg.QoSSampleRate, cfg.BufferMS)
			}
		})
	}
}

// TestPlaybackPreloadReturnsLatestClips 验证预加载按配置条数返回最新的媒体信息。
func TestPlaybackPreloadReturnsLatestClips(t *testing.T) {
	clips := &playbackFakeClips{items: []domplayback.PreloadMedia{
		{ClipID: 101, MediaURL: "/uploads/video/101.mp4", CoverURL: "/uploads/cover/101.jpg", DurationMS: 15000},
		{ClipID: 102, MediaURL: "/uploads/video/102.mp4", CoverURL: "/uploads/cover/102.jpg", DurationMS: 20000},
		{ClipID: 103, MediaURL: "/uploads/video/103.mp4", CoverURL: "/uploads/cover/103.jpg", DurationMS: 25000},
		{ClipID: 104, MediaURL: "/uploads/video/104.mp4", CoverURL: "/uploads/cover/104.jpg", DurationMS: 30000},
		{ClipID: 105, MediaURL: "/uploads/video/105.mp4", CoverURL: "/uploads/cover/105.jpg", DurationMS: 35000},
	}}
	service := appplayback.New(newPlaybackFake(), clips)
	ctx := context.Background()

	if _, err := service.UpsertConfig(ctx, 43, 3, 0.5, 2000); err != nil {
		t.Fatalf("更新预加载条数失败: %v", err)
	}

	items, err := service.GetPreload(ctx, 43)
	if err != nil {
		t.Fatalf("读取预加载列表失败: %v", err)
	}
	if clips.gotLimit != 3 {
		t.Fatalf("预加载数据源应收到 limit=3，实际 %d", clips.gotLimit)
	}
	if len(items) != 3 {
		t.Fatalf("期望返回 3 条媒体信息，实际 %d", len(items))
	}
	for i, want := range []int64{101, 102, 103} {
		if items[i].ClipID != want {
			t.Fatalf("第 %d 条 clip_id 期望 %d，实际 %d", i+1, want, items[i].ClipID)
		}
		if items[i].MediaURL == "" || items[i].CoverURL == "" || items[i].DurationMS <= 0 {
			t.Fatalf("第 %d 条媒体信息字段不完整: %+v", i+1, items[i])
		}
	}
}

// TestPlaybackQoSReportIdempotent 验证同 event_id 重复上报只计一次，不同 event 累加指标。
func TestPlaybackQoSReportIdempotent(t *testing.T) {
	repo := newPlaybackFake()
	service := appplayback.New(repo, nil)
	ctx := context.Background()

	first := &domplayback.QoSReport{ClipID: 21, EventID: "evt-1", LatencyMS: 120, StallCount: 1, Completed: true}
	inserted, err := service.ReportQoS(ctx, 5, first)
	if err != nil {
		t.Fatalf("首次上报失败: %v", err)
	}
	if !inserted {
		t.Fatalf("首次上报期望 inserted=true")
	}
	if first.ReporterID != 5 {
		t.Fatalf("服务应回填上报者 ID，实际 %d", first.ReporterID)
	}

	duplicate := &domplayback.QoSReport{ClipID: 21, EventID: "evt-1", LatencyMS: 500, StallCount: 4, Completed: true}
	inserted, err = service.ReportQoS(ctx, 5, duplicate)
	if err != nil {
		t.Fatalf("重复上报失败: %v", err)
	}
	if inserted {
		t.Fatalf("同 event_id 重复上报期望 inserted=false")
	}
	if repo.qosRows != 1 || repo.latencyTotal != 120 || repo.stallTotal != 1 || repo.completedRows != 1 {
		t.Fatalf("重复上报不应累加指标，实际 rows=%d latency=%d stall=%d completed=%d",
			repo.qosRows, repo.latencyTotal, repo.stallTotal, repo.completedRows)
	}

	another := &domplayback.QoSReport{ClipID: 21, EventID: "evt-2", LatencyMS: 80, StallCount: 2}
	inserted, err = service.ReportQoS(ctx, 5, another)
	if err != nil {
		t.Fatalf("新事件上报失败: %v", err)
	}
	if !inserted {
		t.Fatalf("不同 event_id 期望 inserted=true")
	}
	if repo.qosRows != 2 || repo.latencyTotal != 200 || repo.stallTotal != 3 || repo.completedRows != 1 {
		t.Fatalf("不同事件应累加指标，实际 rows=%d latency=%d stall=%d completed=%d",
			repo.qosRows, repo.latencyTotal, repo.stallTotal, repo.completedRows)
	}
}

// TestPlaybackRejectsInvalidArguments 验证非法用户、空 event_id 与负指标被拒绝。
func TestPlaybackRejectsInvalidArguments(t *testing.T) {
	service := appplayback.New(newPlaybackFake(), nil)
	ctx := context.Background()

	if _, err := service.GetConfig(ctx, 0); !errors.Is(err, domplayback.ErrInvalidConfigUser) {
		t.Fatalf("非法用户期望 ErrInvalidConfigUser，实际 %v", err)
	}
	if _, err := service.ReportQoS(ctx, 0, &domplayback.QoSReport{ClipID: 1, EventID: "x"}); !errors.Is(err, domplayback.ErrInvalidReportTarget) {
		t.Fatalf("上报者非正期望 ErrInvalidReportTarget，实际 %v", err)
	}
	if _, err := service.ReportQoS(ctx, 1, &domplayback.QoSReport{ClipID: 1}); !errors.Is(err, domplayback.ErrEventIDEmpty) {
		t.Fatalf("空 event_id 期望 ErrEventIDEmpty，实际 %v", err)
	}
	if _, err := service.ReportQoS(ctx, 1, &domplayback.QoSReport{ClipID: 1, EventID: "neg", LatencyMS: -1}); !errors.Is(err, domplayback.ErrInvalidMetric) {
		t.Fatalf("负延迟期望 ErrInvalidMetric，实际 %v", err)
	}
}
