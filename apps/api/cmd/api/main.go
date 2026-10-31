// main.go 是 API 服务的主入口，负责加载配置、初始化引擎、注册路由并启动服务。
package main

import (
	"flag"
	"fmt"
	"log"

	"pulsefeed/internal/infra/config"
	"pulsefeed/internal/infra/httpgin"
	"pulsefeed/internal/interfaces/http/router"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	server := httpgin.New()
	router.Register(server.Engine())

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("API 服务启动，监听 %s", addr)

	if err := server.Run(addr); err != nil {
		log.Fatalf("HTTP 服务异常退出: %v", err)
	}
}
