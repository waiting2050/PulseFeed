// gin.go 负责初始化 Gin 引擎，并提供按地址启动 HTTP 服务的方法。
package httpgin

import "github.com/gin-gonic/gin"

// Server 封装 Gin 引擎，供上层注册路由并启动服务。
type Server struct {
	engine *gin.Engine
}

// New 创建 ReleaseMode 下的 Gin 引擎。
func New() *Server {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	engine.Use(gin.Recovery())

	return &Server{engine: engine}
}

// Engine 返回底层 Gin 引擎，用于注册路由。
func (s *Server) Engine() *gin.Engine {
	return s.engine
}

// Run 在 addr 上启动 HTTP 服务，阻塞直到服务退出。
func (s *Server) Run(addr string) error {
	return s.engine.Run(addr)
}
