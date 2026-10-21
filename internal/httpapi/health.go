package httpapi

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"pulsefeed/internal/shared/metrics"
)

const dependencyTimeout = 2 * time.Second

// Dependency 描述一个需要探活的依赖，Check 为 nil 表示该依赖未启用。
type Dependency struct {
	Name  string
	Check func(ctx context.Context) error
}

func mysqlDependency(db *sql.DB) Dependency {
	dep := Dependency{Name: "mysql"}
	if db == nil {
		return dep
	}
	dep.Check = db.PingContext
	return dep
}

func redisDependency(client *redis.Client) Dependency {
	dep := Dependency{Name: "redis"}
	if client == nil {
		return dep
	}
	dep.Check = func(ctx context.Context) error {
		return client.Ping(ctx).Err()
	}
	return dep
}

// liveness 只表示进程存活，不探测下游依赖。
func liveness() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "pulsefeed"})
	}
}

// readiness 探测关键依赖，任一不可用即返回 503，供负载均衡摘除实例。
func readiness(deps []Dependency, registry *metrics.Registry, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), dependencyTimeout)
		defer cancel()

		checks := make(map[string]string, len(deps))
		ready := true
		for _, dep := range deps {
			if dep.Check == nil {
				checks[dep.Name] = "disabled"
				continue
			}
			if err := dep.Check(ctx); err != nil {
				checks[dep.Name] = "down"
				ready = false
				if registry != nil {
					registry.SetDependency(dep.Name, false)
				}
				logger.Warn("dependency check failed", "name", dep.Name, "error", err)
				continue
			}
			checks[dep.Name] = "up"
			if registry != nil {
				registry.SetDependency(dep.Name, true)
			}
		}

		if !ready {
			c.JSON(503, gin.H{"status": "unready", "checks": checks})
			return
		}
		c.JSON(200, gin.H{"status": "ready", "checks": checks})
	}
}
