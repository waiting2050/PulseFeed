// ports.go 定义应用层的可观测性端口：应用服务只依赖该接口，不直接依赖持久化与基础设施实现。
package appobservability

import "time"

// Metrics 是应用层可用的指标采集能力，由组合根注入 infra 层实现。
type Metrics interface {
	// ObserveHTTP 记录一次 HTTP 请求的方法、状态码与耗时。
	ObserveHTTP(method, status string, duration time.Duration)
	// ObserveFeed 记录一次 Feed 读取的场景、结果（ok/error）与耗时。
	ObserveFeed(scene, result string, duration time.Duration)
	// ObserveCache 记录一次 Feed 页缓存访问结果（hit/miss/disabled）。
	ObserveCache(result string)
	// ObserveWorker 记录一次 Worker 消息处理结果（ok/error）。
	ObserveWorker(worker, result string)
	// ObserveQoS 记录一次 QoS 上报结果（inserted/duplicate/error）；
	// 仅 inserted=true 的上报参与延迟、卡顿与完播聚合，避免重复上报重复累计。
	ObserveQoS(result string, latencyMS, stallCount int, completed bool)
}
