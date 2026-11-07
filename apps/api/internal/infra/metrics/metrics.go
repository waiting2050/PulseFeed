// metrics.go 落地 pulsefeed_ 命名空间的 HTTP、Feed、缓存与 Worker 指标，
// 并提供独立注册表与 /metrics 服务入口；API 与 Worker 各自创建实例，避免跨进程串用。
package inframetrics

import (
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 持有一组指标采集器与独立注册表，实现 application/observability.Metrics。
type Metrics struct {
	registry *prometheus.Registry
	up       prometheus.Gauge

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	feedRequests *prometheus.CounterVec
	feedDuration *prometheus.HistogramVec
	cacheEvents  *prometheus.CounterVec
	workerEvents *prometheus.CounterVec

	qosReports   *prometheus.CounterVec
	qosLatency   prometheus.Histogram
	qosStalls    prometheus.Counter
	qosCompleted prometheus.Counter
}

// New 按进程组件名创建指标集合并注册到独立注册表；component 取 api 或 worker。
func New(component string) *Metrics {
	registry := prometheus.NewRegistry()

	upName := "pulsefeed_" + component + "_up"
	if component == "worker" {
		upName = "pulsefeed_worker_up"
	}
	up := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: upName,
		Help: component + " 进程运行状态，1 表示可用。",
	})
	up.Set(1)

	httpRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulsefeed_http_requests_total",
		Help: "HTTP 请求总数，按请求方法与状态码区分。",
	}, []string{"method", "status"})
	httpDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pulsefeed_http_request_duration_seconds",
		Help:    "HTTP 请求耗时分布，按请求方法与状态码区分。",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "status"})
	feedRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulsefeed_feed_requests_total",
		Help: "Feed 读取次数，按场景与结果区分。",
	}, []string{"scene", "result"})
	feedDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pulsefeed_feed_request_duration_seconds",
		Help:    "Feed 读取耗时分布，按场景区分。",
		Buckets: prometheus.DefBuckets,
	}, []string{"scene"})
	cacheEvents := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulsefeed_feed_cache_events_total",
		Help: "Feed 页缓存访问次数，按 hit/miss/disabled 区分。",
	}, []string{"result"})
	workerEvents := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulsefeed_worker_events_total",
		Help: "Worker 消息处理次数，按 Worker 名称与结果区分。",
	}, []string{"worker", "result"})
	qosReports := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulsefeed_playback_qos_reports_total",
		Help: "QoS 上报次数，按结果（inserted/duplicate/error）区分。",
	}, []string{"result"})
	qosLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "pulsefeed_playback_qos_latency_milliseconds",
		Help:    "QoS 上报的首帧延迟分布（毫秒），仅统计首次落库的上报。",
		Buckets: []float64{10, 50, 100, 200, 500, 1000, 2000, 5000},
	})
	qosStalls := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pulsefeed_playback_qos_stalls_total",
		Help: "QoS 上报累计卡顿次数，仅统计首次落库的上报。",
	})
	qosCompleted := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pulsefeed_playback_qos_completed_total",
		Help: "QoS 上报中标记完播的次数，仅统计首次落库的上报。",
	})

	registry.MustRegister(
		up,
		httpRequests,
		httpDuration,
		feedRequests,
		feedDuration,
		cacheEvents,
		workerEvents,
		qosReports,
		qosLatency,
		qosStalls,
		qosCompleted,
	)

	return &Metrics{
		registry:     registry,
		up:           up,
		httpRequests: httpRequests,
		httpDuration: httpDuration,
		feedRequests: feedRequests,
		feedDuration: feedDuration,
		cacheEvents:  cacheEvents,
		workerEvents: workerEvents,
		qosReports:   qosReports,
		qosLatency:   qosLatency,
		qosStalls:    qosStalls,
		qosCompleted: qosCompleted,
	}
}

// ObserveHTTP 记录一次 HTTP 请求的方法、状态码与耗时。
func (m *Metrics) ObserveHTTP(method, status string, duration time.Duration) {
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(method, status).Inc()
	m.httpDuration.WithLabelValues(method, status).Observe(duration.Seconds())
}

// ObserveFeed 记录一次 Feed 读取的场景、结果与耗时。
func (m *Metrics) ObserveFeed(scene, result string, duration time.Duration) {
	if m == nil {
		return
	}
	m.feedRequests.WithLabelValues(scene, result).Inc()
	m.feedDuration.WithLabelValues(scene).Observe(duration.Seconds())
}

// ObserveCache 记录一次 Feed 页缓存访问结果。
func (m *Metrics) ObserveCache(result string) {
	if m == nil {
		return
	}
	m.cacheEvents.WithLabelValues(result).Inc()
}

// ObserveWorker 记录一次 Worker 消息处理结果。
func (m *Metrics) ObserveWorker(worker, result string) {
	if m == nil {
		return
	}
	m.workerEvents.WithLabelValues(worker, result).Inc()
}

// ObserveQoS 记录一次 QoS 上报；延迟、卡顿与完播只在首次落库（inserted）时聚合，
// 重复上报仅累加 reports_total，避免幂等路径把同一播放重复计入质量指标。
func (m *Metrics) ObserveQoS(result string, latencyMS, stallCount int, completed bool) {
	if m == nil {
		return
	}
	m.qosReports.WithLabelValues(result).Inc()
	if result != "inserted" {
		return
	}
	m.qosLatency.Observe(float64(latencyMS))
	if stallCount > 0 {
		m.qosStalls.Add(float64(stallCount))
	}
	if completed {
		m.qosCompleted.Inc()
	}
}

// Handler 返回当前注册表的 /metrics HTTP 处理器。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry 返回当前实例的注册表，供 Worker 独立暴露指标。
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// RunServer 在指定地址暴露 /metrics 并阻塞运行，供 Worker 主入口在独立 goroutine 中调用。
func RunServer(addr string, registry *prometheus.Registry) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	log.Printf("Worker metrics 启动，监听 %s", addr)
	return http.ListenAndServe(addr, mux)
}
