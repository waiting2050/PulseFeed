// metrics.go 为 Worker 提供独立 metrics 端口，先落地运行状态指标，S17 再统一收口全量指标。
package inframetrics

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// workerUp 表示 Worker 进程存活，固定为 1。
var workerUp = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "pulsefeed_worker_up",
	Help: "Worker 进程运行状态，1 表示可用。",
})

// registry 是 Worker 独立注册表，避免与 API 进程指标串用。
var registry = prometheus.NewRegistry()

func init() {
	workerUp.Set(1)
	registry.MustRegister(workerUp)
}

// RunServer 在指定地址暴露 /metrics 并阻塞运行，供 Worker 主入口在独立 goroutine 中调用。
func RunServer(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	log.Printf("Worker metrics 启动，监听 %s", addr)
	return http.ListenAndServe(addr, mux)
}
