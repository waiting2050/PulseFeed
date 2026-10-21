package metrics

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "pulsefeed"

// Registry 保存服务自身的指标注册表，避免依赖全局默认注册表，便于测试隔离。
type Registry struct {
	registry     *prometheus.Registry
	requests     *prometheus.CounterVec
	duration     *prometheus.HistogramVec
	inFlight     prometheus.Gauge
	rateLimited  *prometheus.CounterVec
	dependencyUp *prometheus.GaugeVec
}

func New(version, appEnv string) *Registry {
	registry := prometheus.NewRegistry()
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "构建信息，值恒为 1",
	}, []string{"version", "env"})
	buildInfo.WithLabelValues(version, appEnv).Set(1)

	r := &Registry{
		registry: registry,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_total",
			Help:      "按路由和状态码统计的 HTTP 请求数",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP 请求耗时分布",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "http_in_flight_requests",
			Help:      "当前正在处理的 HTTP 请求数",
		}),
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_rate_limited_total",
			Help:      "被限流拒绝的请求数",
		}, []string{"route"}),
		dependencyUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "dependency_up",
			Help:      "依赖健康状态，1 表示可用，0 表示不可用",
		}, []string{"name"}),
	}

	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
		r.requests,
		r.duration,
		r.inFlight,
		r.rateLimited,
		r.dependencyUp,
	)
	return r
}

// Handler 返回 Prometheus 文本格式的采集端点。
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
}

func (r *Registry) IncInFlight() {
	r.inFlight.Inc()
}

func (r *Registry) DecInFlight() {
	r.inFlight.Dec()
}

// ObserveRequest 记录一次请求的状态码与耗时，route 使用路由模板避免标签基数膨胀。
func (r *Registry) ObserveRequest(method, route string, status int, seconds float64) {
	r.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	r.duration.WithLabelValues(method, route).Observe(seconds)
}

func (r *Registry) ObserveRateLimited(route string) {
	r.rateLimited.WithLabelValues(route).Inc()
}

func (r *Registry) SetDependency(name string, up bool) {
	value := 0.0
	if up {
		value = 1
	}
	r.dependencyUp.WithLabelValues(name).Set(value)
}
