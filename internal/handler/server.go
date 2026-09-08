// Package handler HTTP 层（薄：绑定/映射，业务在 service——结构基线）。
// M-A1 仅健康探针；/api/v1 业务组随 M-A2（admin/types）起挂载，
// AK/SK 验签与访问日志中间件随 M-A6。
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tracerbiubiubiu/activelist/internal/middleware"
)

// Deps handler 依赖。Ready 为 readyz 探针（检 PG 可查询）；nil = 恒就绪（测试用）。
type Deps struct {
	Ready func() error
}

// New 构造路由引擎。
func New(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID())

	// 探针裸露（不进验签组）——容器编排存活/就绪检查用。
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) {
		if d.Ready != nil {
			if err := d.Ready(); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unready", "err": err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	return r
}
