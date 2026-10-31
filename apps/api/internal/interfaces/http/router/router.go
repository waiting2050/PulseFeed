// router.go 负责注册 HTTP 路由，并提供健康检查与 Prometheus 指标端点。
package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Register 将健康检查和指标路由挂载到给定的 Gin 引擎上。
func Register(engine *gin.Engine) {
	registry := prometheus.NewRegistry()

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulsefeed_http_requests_total",
		Help: "HTTP 请求总数，按请求方法与状态码区分。",
	}, []string{"method", "status"})

	serviceUp := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pulsefeed_service_up",
		Help: "服务运行状态，1 表示可用。",
	})
	serviceUp.Set(1)

	registry.MustRegister(requests, serviceUp)

	engine.Use(countRequests(requests))
	engine.GET("/health", health)
	engine.GET("/metrics", gin.WrapH(promhttp.HandlerFor(registry, promhttp.HandlerOpts{})))
}

// health 返回服务健康状态。
func health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": gin.H{
			"status": "ok",
		},
	})
}

// countRequests 统计每个请求的方法与响应状态码。
func countRequests(counter *prometheus.CounterVec) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		counter.WithLabelValues(c.Request.Method, strconv.Itoa(c.Writer.Status())).Inc()
	}
}
